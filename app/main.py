"""FastAPI 应用入口：初始化、路由挂载、静态资源、中文请求日志、全局异常兜底。"""
import logging
import time

from fastapi import FastAPI, Request
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import FileResponse, JSONResponse
from fastapi.staticfiles import StaticFiles
from starlette.middleware.trustedhost import TrustedHostMiddleware

from app.config import STATIC_DIR
from app.models.db import init_db
from app.version import APP_NAME, APP_VERSION

# 全局中文日志：每个 API 请求打印「调用了什么接口」，写操作在业务内打印「更新了什么」
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(name)s] %(levelname)s %(message)s",
)
# uvicorn 默认 access log 形如「GET /api/status/jobs HTTP/1.1」——关掉，改走下方中文请求日志
logging.getLogger("uvicorn.access").setLevel(logging.WARNING)
logger = logging.getLogger("api")

# 前端轮询/健康检查：不打请求日志，避免刷屏
_QUIET_PATHS = frozenset({
    "/api/status/jobs",
    "/api/status/prewarm",
    "/api/health",
})


def create_app() -> FastAPI:
    # 打包无控制台态可能 stdout/stderr=None；必须在预热线程拉 akshare 前补齐，否则 tqdm 崩。
    from app.config import IS_PACKAGED, SKIP_STARTUP_TASKS

    if IS_PACKAGED:
        from app.windows_launcher import ensure_stdio

        ensure_stdio()

    app = FastAPI(title=APP_NAME, version=APP_VERSION)

    # 启动时建表（幂等）
    init_db()

    # 启动后后台任务（不阻塞启动）：
    #   1) 强制拉一遍港股汇率（只有「全量刷新」才会再拉）
    #   2) 今天有分红除权的持仓自动摊薄成本（幂等）
    #   3) 预热 A股+港股全市场列表（搜索/名称回填只读缓存，绝不联网）
    try:
        import threading

        def _startup_tasks():
            # 预热进度写入 app.prewarm，前端首页提示条轮询 /api/status/prewarm 展示
            from app.prewarm import begin, complete, finish, mark

            begin()
            logger.info("[预热] 启动后台预热（不阻塞服务）：①港股汇率 ②今日除权 ③全市场列表缓存")
            try:
                from app.services.fx import refresh_hk_fx

                mark("拉取港股汇率")
                r = refresh_hk_fx(force=True)
                if r.get("fetched"):
                    logger.info("[预热] 港股汇率已拉取 %d 项，回填 %d 笔港股金额",
                                r["fetched"], r.get("backfilled", 0))
                elif r.get("currency") is None:
                    logger.info("[预热] 港股汇率：无港股持仓，跳过")
                else:
                    logger.info("[预热] 港股汇率已有当日值，跳过")
            except Exception:  # noqa: BLE001 汇率拉取失败不影响服务
                logger.warning("[预热] 港股汇率拉取失败（不影响服务，可稍后全量刷新重试）")
            complete("港股汇率")

            try:
                from app.services.dividend import apply_dividend_adjustments

                mark("检查今日除权")
                apply_dividend_adjustments()   # 成功/失败内部已有日志
            except Exception:  # noqa: BLE001 除权检查失败不影响服务
                logger.warning("[预热] 今日除权检查失败（不影响服务）")
            complete("今日除权")

            try:
                from app.api.stocks import preload_market_lists

                mark("缓存全市场列表")
                counts = preload_market_lists()
                total = sum(counts.values())
                if total:
                    logger.info(
                        "[预热] 全市场列表缓存就绪：A股 %d / ETF %d / 港股 %d，搜索与名称回填可用",
                        counts.get("a", 0), counts.get("etf", 0), counts.get("hk", 0),
                    )
                else:
                    logger.warning(
                        "[预热] 市场列表全部为空，名称搜索不可用。"
                        "常见原因：安装包无控制台模式下进度条库崩溃（请升级安装包），"
                        "或网络失败。可退出后重新打开应用重试。"
                    )
            except Exception:  # noqa: BLE001 市场列表预热失败不影响服务
                logger.exception("[预热] 市场列表预热失败（不影响服务，可重启重试）")
            complete("全市场列表")

            try:
                from app.services.indices import auto_map_holdings_etfs, refresh_all_indices

                mark("预热指数")
                r = refresh_all_indices()
                auto_map_holdings_etfs()
                logger.info("[预热] 指数预热完成：%d 个成功，失败 %d 个（%s），ETF 自动映射已跑",
                            r.get("ok", 0), len(r.get("fail", [])), r.get("fail", []))
            except Exception:  # noqa: BLE001 指数预热失败不影响服务
                logger.warning("[预热] 指数预热失败（不影响服务，可手动刷新）")
            complete("指数")

            try:
                from app.services.ai_scoring import catchup_pending_daily

                mark("补打今日 AI 评分")
                catchup_pending_daily()   # 已收盘 + 有交易 + 无报告 → 后台补打一次（内部有守卫）
            except Exception:  # noqa: BLE001 补打失败不影响服务
                logger.warning("[预热] 补打今日 AI 评分失败（不影响服务，可手动重打）")
            complete("今日 AI 评分")

            finish()

        if not SKIP_STARTUP_TASKS:
            threading.Thread(target=_startup_tasks, daemon=True).start()
    except Exception:  # noqa: BLE001
        pass

    # 请求日志（在 CORS 之后 add，即最先执行）：记录方法/路径/查询/状态/耗时
    # 轮询类接口静默（jobs/prewarm/health），否则每秒刷屏
    @app.middleware("http")
    async def log_requests(request, call_next):  # noqa: ANN001
        start = time.time()
        quiet = request.url.path in _QUIET_PATHS
        try:
            response = await call_next(request)
        except Exception:
            logger.exception("[请求] %s %s 处理异常", request.method, request.url.path)
            raise
        if not quiet:
            dur_ms = int((time.time() - start) * 1000)
            logger.info(
                "[请求] %s %s%s → %s（%dms）",
                request.method, request.url.path,
                f"?{request.url.query}" if request.url.query else "",
                response.status_code, dur_ms,
            )
        return response

    # 桌面版只绑定 loopback；同时限制 Host 与浏览器跨域来源，避免其他网站操作本机 API。
    app.add_middleware(
        TrustedHostMiddleware,
        allowed_hosts=["127.0.0.1", "localhost", "testserver"],
        www_redirect=False,
    )
    app.add_middleware(
        CORSMiddleware,
        allow_origin_regex=r"^https?://(127\.0\.0\.1|localhost)(:\d+)?$",
        allow_methods=["*"],
        allow_headers=["*"],
    )

    # API 路由
    from app.api import ai, ai_scoring, index, stocks, system, trades, portfolio, holdings

    for mod in (system, holdings, trades, stocks, portfolio, index, ai, ai_scoring):
        app.include_router(mod.router, prefix="/api")

    # 全局异常兜底：未捕获异常 → HTTP 500 + 明确原因（前端 api.js 读 detail 展示），并打印完整堆栈
    @app.exception_handler(Exception)
    async def unhandled_exception_handler(request: Request, exc: Exception):
        logger.exception("[接口异常] %s %s → %s", request.method, request.url.path, exc)
        return JSONResponse(
            status_code=500,
            content={"ok": False, "detail": f"{type(exc).__name__}: {exc}"},
        )

    # 前端静态资源
    if not STATIC_DIR.is_dir():
        raise RuntimeError(f"静态资源目录不存在：{STATIC_DIR}")
    app.mount("/static", StaticFiles(directory=str(STATIC_DIR)), name="static")

    @app.get("/")
    def index():
        return FileResponse(STATIC_DIR / "index.html")

    return app


app = create_app()
