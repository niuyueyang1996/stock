"""接口层：真实访问底层接口（akshare/原始 HTTP），返回原始 dict/DataFrame，无业务口径。

各平台独立文件：raw_em / raw_sina / raw_tencent / raw_baidu / raw_mock。
只做请求与最小解析，不折算货币、不规整字段——那是数据转换层（normalizers.py）的事。
"""
