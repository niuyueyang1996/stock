"""FastAPI 应用入口：初始化、路由挂载、静态资源、中文请求日志、全局异常兜底。"""
import logging
import time

from fastapi import FastAPI, Request
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import FileResponse, JSONResponse
from fastapi.staticfiles import StaticFiles

from app.config import STATIC_DIR
from app.models.db import init_db

# 全局中文日志：每个 API 请求打印「调用了什么接口」，写操作在业务内打印「更新了什么」
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(name)s] %(levelname)s %(message)s",
)
logger = logging.getLogger("api")


def create_app() -> FastAPI:
    app = FastAPI(title="个人自建ETF持仓分析", version="0.1.0")

    # 启动时建表（幂等）
    init_db()

    # 启动后后台任务（不阻塞启动）：
    #   1) 强制拉一遍港股汇率（只有「全量刷新」才会再拉）
    #   2) 今天有分红除权的持仓自动摊薄成本（幂等）
    try:
        import threading

        def _startup_tasks():
            try:
                from app.services.fx import refresh_hk_fx

                refresh_hk_fx(force=True)
            except Exception:  # noqa: BLE001 汇率拉取失败不影响服务
                pass
            try:
                from app.services.dividend import apply_dividend_adjustments

                apply_dividend_adjustments()
            except Exception:  # noqa: BLE001 除权检查失败不影响服务
                pass

        threading.Thread(target=_startup_tasks, daemon=True).start()
    except Exception:  # noqa: BLE001
        pass

    # 请求日志（在 CORS 之后 add，即最先执行）：记录方法/路径/查询/状态/耗时
    @app.middleware("http")
    async def log_requests(request, call_next):  # noqa: ANN001
        start = time.time()
        try:
            response = await call_next(request)
        except Exception:
            logger.exception("[请求] %s %s 处理异常", request.method, request.url.path)
            raise
        dur_ms = int((time.time() - start) * 1000)
        logger.info(
            "[请求] %s %s%s → %s（%dms）",
            request.method, request.url.path,
            f"?{request.url.query}" if request.url.query else "", response.status_code, dur_ms,
        )
        return response

    # CORS（前端开发用）
    app.add_middleware(
        CORSMiddleware,
        allow_origins=["*"],
        allow_methods=["*"],
        allow_headers=["*"],
    )

    # API 路由
    from app.api import scoring, stocks, system, trades, portfolio, holdings

    for mod in (system, holdings, trades, stocks, portfolio, scoring):
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
    STATIC_DIR.mkdir(parents=True, exist_ok=True)
    app.mount("/static", StaticFiles(directory=str(STATIC_DIR)), name="static")

    @app.get("/")
    def index():
        return FileResponse(STATIC_DIR / "index.html")

    return app


app = create_app()
