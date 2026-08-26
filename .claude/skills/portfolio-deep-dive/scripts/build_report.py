#!/usr/bin/env python3
"""build_report.py — 将 metrics.json 注入模板，导出 PDF。"""
import json, pathlib, datetime, sys, subprocess

def build(metrics_path, template_path, out_html, out_pdf=None):
    metrics=json.loads(pathlib.Path(metrics_path).read_text(encoding="utf-8")) if pathlib.Path(metrics_path).exists() else {}
    tpl=pathlib.Path(template_path).read_text(encoding="utf-8")
    data={"period": metrics.get("period","—"), "gen_time": datetime.datetime.now().strftime("%Y-%m-%d %H:%M"), **metrics}
    inject=f"<script>window.REPORT_DATA={json.dumps(data, ensure_ascii=False)};</script>"
    html=tpl.replace("</body>", inject+"</body>")
    pathlib.Path(out_html).write_text(html, encoding="utf-8")
    print(f"HTML -> {out_html}")
    if out_pdf:
        # 优先用 Chrome headless，退化用 wkhtmltopdf
        try:
            subprocess.run(["google-chrome","--headless","--disable-gpu","--no-sandbox",
                            f"--print-to-pdf={out_pdf}", out_html], check=True, timeout=60)
        except Exception as e:
            print(f"chrome failed: {e}, try wkhtmltopdf")
            subprocess.run(["wkhtmltopdf", out_html, out_pdf], check=True, timeout=60)
        print(f"PDF -> {out_pdf}")

if __name__=="__main__":
    build(sys.argv[1], sys.argv[2], sys.argv[3], sys.argv[4] if len(sys.argv)>4 else None)
