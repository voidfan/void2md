#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""golden 回放：把冻结的 36 条服务级用例打到真实运行的服务上，逐条比对。

这是 tools/oracle/e2e_test.go 的**离线替代品**，给「测试机上没有 Go 工具链」的场景用。
权威判据仍然是那个 Go 测试：

    DATIVE_E2E_OFFLINE=1 DATIVE_GO_BASE_URL=http://127.0.0.1:3000 \
      go test ./tools/oracle/ -run TestE2EServiceParity -v

本脚本复刻它的比对语义（e2e_test.go 的 compare / jsonDiff / decodeJSON）：
    1) 状态码必须相同；
    2) COMPARE_HEADERS 里列出的响应头必须相同（比对前把 base URL 归一成 {{BASE}}）；
    3) 响应体：能解析成 JSON 就做深比较（数字按**字面量**比，不通分 100 与 100.0），
       否则要求字节相同；JSON 深比较通过后再补一条**逐字节**判据，用来抓
       「键序/空白/转义不同、语义相同」这种深比较看不出来的差异。
    4) 307 重定向**不跟随**，直接看原始响应（Go 侧 CheckRedirect 返回 ErrUseLastResponse）。

期望值 100% 来自 golden/e2e_service.json（原 Python 服务现场采集，不是手写）。
只有两处 golden 里没有、必须从 Go 源码抄过来，改那边记得同步这里：
    - COMPARE_HEADERS  <- e2e_test.go:99,101,103,169,172,179,189
    - CORS_REQ_HEADERS <- e2e_test.go:167-168,174-178,185-188

用法：
    python3 replay_golden.py [base_url] [--golden 路径] [-v]
    python3 replay_golden.py http://127.0.0.1:3000
退出码：0 = 全部一致；1 = 有 FAIL。
"""
import json
import os
import sys
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_GOLDEN = os.path.join(HERE, "golden", "e2e_service.json")

# 哪些响应头参与比对（键 = 用例名，小写头名）。抄自 e2e_test.go 的 CompareHeaders。
COMPARE_HEADERS = {
    "method_not_allowed_405": ["allow"],
    "head_healthz_405": ["allow"],
    "trailing_slash_redirect": ["location"],
    "cors_simple_request": [
        "access-control-allow-origin", "access-control-allow-credentials", "vary",
    ],
    "cors_simple_404": [
        "access-control-allow-origin", "access-control-allow-credentials", "vary",
    ],
    "cors_preflight": [
        "access-control-allow-origin", "access-control-allow-credentials",
        "access-control-allow-methods", "access-control-allow-headers",
        "access-control-max-age", "vary", "content-type",
    ],
    "cors_preflight_disallowed_method": [
        "access-control-allow-origin", "vary", "content-type",
    ],
}

# CORS 用例的请求头（golden 里不记录请求头）。抄自 e2e_test.go 的 pureCases。
CORS_REQ_HEADERS = {
    "cors_simple_request": {"Origin": "http://x.test"},
    "cors_simple_404": {"Origin": "http://x.test"},
    "cors_preflight": {
        "Origin": "http://x.test",
        "Access-Control-Request-Method": "POST",
        "Access-Control-Request-Headers": "content-type",
    },
    "cors_preflight_disallowed_method": {
        "Origin": "http://x.test",
        "Access-Control-Request-Method": "TRACE",
    },
}

# 不在 golden 里的端点（e2e_test.go 的 skippedCases），显式列出来，
# 免得「36 全绿」被误读成「服务全功能验证过了」。
NOT_COVERED = [
    "conn_test / get_metadata / get_metadata_with_value_examples / get_server_version",
    "sql_query / query_by_nl / excel_upload / excel_delete",
    "openapi.json / docs / redoc",
]


# --------------------------------------------------------------------------
# 数字字面量：Go 侧用 json.Decoder.UseNumber()，数字按原字面量比较，
# 100 与 100.0 是**不同**的。这里用 str 子类承载字面量，既保字面量又能与真实
# 字符串区分开（typeName）。
# --------------------------------------------------------------------------
class Num(str):
    pass


DECODER = json.JSONDecoder(parse_int=Num, parse_float=Num)


def decode_json(s):
    """对应 e2e_test.go 的 decodeJSON：只允许一个 JSON 值，多余内容视为非 JSON。"""
    try:
        v, idx = DECODER.raw_decode(s)
    except ValueError as e:
        return None, e
    if s[idx:].strip() != "":
        return None, ValueError("响应体含多余内容")
    return v, None


def type_name(v):
    if isinstance(v, Num):
        return "number"
    if isinstance(v, bool):
        return "bool"
    if v is None:
        return "null"
    if isinstance(v, dict):
        return "object"
    if isinstance(v, list):
        return "array"
    if isinstance(v, str):
        return "string"
    return type(v).__name__


def compact(v):
    """对应 compactJSON：仅用于差异消息与标量值的精确比较。"""
    if isinstance(v, Num):
        return str(v)
    return json.dumps(v, ensure_ascii=False, separators=(",", ":"))


def json_diff(path, want, got, out):
    """对应 e2e_test.go 的 jsonDiff。"""
    if isinstance(want, dict):
        if not isinstance(got, dict):
            out.append("%s: 期望 object，实际 %s" % (path, type_name(got)))
            return
        for k in sorted(set(want) | set(got)):
            if k in want and k not in got:
                out.append("%s.%s: 服务缺失（golden = %s）" % (path, k, compact(want[k])))
            elif k not in want and k in got:
                out.append("%s.%s: 服务多出（服务 = %s）" % (path, k, compact(got[k])))
            else:
                json_diff("%s.%s" % (path, k), want[k], got[k], out)
    elif isinstance(want, list):
        if not isinstance(got, list):
            out.append("%s: 期望 array，实际 %s" % (path, type_name(got)))
            return
        if len(want) != len(got):
            out.append("%s: 数组长度 期望 %d，实际 %d（golden = %s；服务 = %s）"
                       % (path, len(want), len(got), compact(want), compact(got)))
        for i in range(min(len(want), len(got))):
            json_diff("%s[%d]" % (path, i), want[i], got[i], out)
    else:
        if type_name(want) != type_name(got) or compact(want) != compact(got):
            out.append("%s: 期望 %s(%s)，实际 %s(%s)"
                       % (path, compact(want), type_name(want), compact(got), type_name(got)))


# --------------------------------------------------------------------------
# HTTP
# --------------------------------------------------------------------------
class NoRedirect(urllib.request.HTTPRedirectHandler):
    """不跟随重定向：307 要能拿到原始响应。"""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


OPENER = urllib.request.build_opener(NoRedirect)


def send(base, case):
    """发一条请求，返回 (status, headers: 小写名->首个值, body: str)。

    404/405 等在 urllib 里是 HTTPError，但那就是我们要看的响应本身。
    """
    body = case.get("request_body", "")
    data = body.encode("utf-8") if body else None
    headers = {}
    if body:
        headers["Content-Type"] = "application/json"
    headers.update(CORS_REQ_HEADERS.get(case["name"], {}))
    req = urllib.request.Request(base + case["path"], data=data,
                                 headers=headers, method=case["method"])
    try:
        resp = OPENER.open(req, timeout=30)
        status, hdrs, raw = resp.status, resp.headers, resp.read()
    except urllib.error.HTTPError as e:
        status, hdrs, raw = e.code, e.headers, e.read()
    out = {}
    for k in set(hdrs.keys()):
        vals = hdrs.get_all(k) or []
        out[k.lower()] = vals[0] if vals else ""
    return status, out, raw.decode("utf-8", "replace")


def norm(s, base):
    return s.replace(base, "{{BASE}}")


def compare(case, base, status, headers, body):
    """对应 e2e_test.go 的 compare，差异以字符串列表返回。"""
    diffs = []
    want_status = case["status"]
    if want_status != status:
        diffs.append("status: 期望 %d，实际 %d" % (want_status, status))

    for h in COMPARE_HEADERS.get(case["name"], []):
        want = norm(case["headers"].get(h, ""), "http://127.0.0.1:3000")
        got = norm(headers.get(h, ""), base)
        if want != got:
            diffs.append("header %s: 期望 %r，实际 %r" % (h, want, got))

    want_body = norm(case["response_body"], "http://127.0.0.1:3000")
    got_body = norm(body, base)
    wj, werr = decode_json(want_body)
    gj, gerr = decode_json(got_body)
    if werr is None and gerr is None:
        json_diff("$", wj, gj, diffs)
    elif werr is not None and gerr is not None:
        if want_body != got_body:
            diffs.append("body: 非 JSON 且不一致\n    期望 %r\n    实际 %r" % (want_body, got_body))
    elif werr is not None:
        diffs.append("body: golden 非 JSON（%s），服务是 JSON：%s" % (werr, compact(gj)))
    else:
        diffs.append("body: golden 是 JSON，服务非 JSON（%s）：%r" % (gerr, got_body))

    # 逐字节判据：深比较通过但字节不同 -> 键序/空白/转义差异（曾出过假绿的地方）。
    if not diffs and want_body != got_body:
        diffs.append("body 逐字节不一致（JSON 深比较通过，说明只是键序/空白/转义差异）"
                     "\n    期望 %s\n    实际 %s" % (want_body, got_body))
    return diffs


def main():
    args = sys.argv[1:]
    base = "http://127.0.0.1:3000"
    golden_path = DEFAULT_GOLDEN
    verbose = False
    while args:
        a = args.pop(0)
        if a == "--golden":
            golden_path = args.pop(0)
        elif a in ("-v", "--verbose"):
            verbose = True
        else:
            base = a
    base = base.rstrip("/")

    with open(golden_path, encoding="utf-8") as f:
        golden = json.load(f)
    cases = golden["cases"]

    print("==> 回放 %d 条 golden 用例 -> %s" % (len(cases), base))
    print("    golden: %s" % golden_path)
    print("    note: %s" % golden.get("note", "")[:60])
    print("")

    # 先探活，免得后面 36 条全是「连不上」的噪音。
    try:
        status, _, body = send(base, {"name": "_probe", "method": "GET", "path": "/healthz"})
        print("==> 探活 GET /healthz -> %d %s" % (status, body.strip()))
        if status != 200 or body.strip() != '"ok"':
            print("    注意：探活未通过，后面的 FAIL 可能都只是连不上/服务异常。")
    except Exception as e:  # noqa: BLE001 - 探活失败直接给结论，别继续
        print("==> 探活失败：%s" % e)
        print("    服务不可达，请先确认容器在跑、端口映射正确。")
        return 1

    ok = fails = 0
    for case in cases:
        tmp_h = {"name": case["name"], "method": case["method"], "path": case["path"]}
        if "request_body" in case:
            tmp_h["request_body"] = case["request_body"]
        try:
            status, headers, body = send(base, tmp_h)
            diffs = compare(case, base, status, headers, body)
        except Exception as e:  # noqa: BLE001 - 单条异常不该中断整轮
            diffs = ["请求异常：%r" % e]
        if diffs:
            fails += 1
            print("FAIL %s %s %s" % (case["method"], case["path"], case["name"]))
            for d in diffs:
                print("       %s" % d)
        else:
            ok += 1
            msg = "PASS %s" % case["name"]
            if verbose:
                msg += "  (%d, %d 字节)" % (status, len(body))
            print(msg)

    print("")
    print("==> 汇总：%d 个用例，%d 一致，%d 不一致" % (len(cases), ok, fails))
    print("")
    print("==> 本判据**不覆盖**下列端点（需外部 DB/LLM，或不在对齐范围）：")
    for line in NOT_COVERED:
        print("    - %s" % line)
    print("    这些端点请看 README 的数据源/LLM 环境变量，手工验证。")
    return 1 if fails else 0


if __name__ == "__main__":
    sys.exit(main())
