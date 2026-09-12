# dropframe-api

广播级丢帧时间码（Drop-Frame Timecode）与真实帧序号的纯后端换算服务。解决素材清单把丢帧时间码当作连续计数导致的定位偏差：节目越长，时间码标签与真实帧序号的差值越大，必须按丢帧规则换算才能精确定位。

- Go 1.25 + Gin，无外部服务依赖
- 支持 `30000/1001`（名义 30 fps，每分钟丢 2 个标签）与 `60000/1001`（名义 60 fps，每分钟丢 4 个标签）
- 正向：时间码 → 从当天 `00:00:00;00` 起、首帧为 0 的整数帧序号
- 反向：帧序号 → 当日唯一合法时间码标签
- 跨度：两个时间码 → 实际帧间隔（不含起点帧），可选跨零点续算，避免客户端各自处理日期翻转
- 全部换算基于公式实时计算，正反两个方向互为逆运算，可逆定位

## 丢帧规则

每个小时内，除分钟数能被 10 整除的分钟（00、10、20、30、40、50）外，其余分钟的 00 秒须跳过前 2 个（30 fps）或前 4 个（60 fps）帧标签。例如 30 fps 下 `00:01:00;00` 与 `00:01:00;01` 不存在，该分钟第一个合法标签是 `00:01:00;02`；而 `00:10:00;00`、`00:10:00;01` 均合法。被跳过的标签一律视为非法输入。

帧序号当日合法范围为 `[0, 2589407]`（30 fps）与 `[0, 5178815]`（60 fps），即最后一帧分别为 `23:59:59;29` 与 `23:59:59;59`。负数或超出当日最后一帧的序号返回 422。

## 快速开始

```bash
# 启动 API 与一次性验收服务（验收通过后 verify 容器自动退出）
docker compose up --build

# 覆盖宿主端口（默认 8080）
API_PORT=9000 docker compose up --build

# 只跑验收
docker compose up --build --abort-on-container-exit verify
```

`verify` 服务等待 API 健康后执行验收：边界向量、丢帧标签非法性、1440 次分钟连续衔接（含十分钟边界）、时间码跨度（同日、跨午夜、零跨度与未授权跨日拒绝）、错误响应格式、抽样回环可逆性，全部通过则以退出码 0 结束，否则非 0。

本地开发（需要 Go 1.25）：

```bash
go test ./...        # 单元测试（含当日全量帧序号回环验证）
go run ./cmd/api     # 监听 :8080，可用 LISTEN_ADDR 覆盖
```

## 请求示例

`POST /api/v1/convert`，请求体包含 `direction`、`rate`，以及按方向选择的 `timecode`、`frame_index` 或 `start_timecode` + `end_timecode`（可选 `next_day`）。

正向换算（时间码 → 帧序号）：

```bash
curl -s -X POST http://localhost:8080/api/v1/convert \
  -H 'Content-Type: application/json' \
  -d '{"direction":"timecode_to_frame","rate":"30000/1001","timecode":"00:01:00;02"}'
```

```json
{"frame_index": 1800}
```

反向换算（帧序号 → 时间码）：

```bash
curl -s -X POST http://localhost:8080/api/v1/convert \
  -H 'Content-Type: application/json' \
  -d '{"direction":"frame_to_timecode","rate":"60000/1001","frame_index":35964}'
```

```json
{"timecode": "00:10:00;00"}
```

跨度换算（两个时间码 → 实际帧间隔，不含起点帧）：

```bash
curl -s -X POST http://localhost:8080/api/v1/convert \
  -H 'Content-Type: application/json' \
  -d '{"direction":"timecode_span","rate":"30000/1001","start_timecode":"00:09:59;29","end_timecode":"00:10:00;01"}'
```

```json
{"elapsed_frames": 2}
```

终点标签早于起点时，仅当显式提交 `next_day=true` 才按当日总帧数跨零点续算（跨度始终不超过一天），否则返回 422 `END_BEFORE_START`：

```bash
curl -s -X POST http://localhost:8080/api/v1/convert \
  -H 'Content-Type: application/json' \
  -d '{"direction":"timecode_span","rate":"30000/1001","start_timecode":"23:59:59;29","end_timecode":"00:00:00;00","next_day":true}'
```

```json
{"elapsed_frames": 1}
```

请求被跳过的丢帧标签：

```bash
curl -s -X POST http://localhost:8080/api/v1/convert \
  -H 'Content-Type: application/json' \
  -d '{"direction":"timecode_to_frame","rate":"30000/1001","timecode":"00:01:00;01"}'
```

```json
{"error": {"code": "DROPPED_FRAME_LABEL", "field": "timecode", "message": "timecode \"00:01:00;01\" is a label skipped by the drop-frame rule at rate 30000/1001 and does not exist"}}
```

## 字段约束

| 字段 | 约束 |
| --- | --- |
| `direction` | `timecode_to_frame`、`frame_to_timecode` 或 `timecode_span` |
| `rate` | `30000/1001` 或 `60000/1001` |
| `timecode` | 严格 `HH:MM:SS;FF`；HH 00-23，MM/SS 00-59；FF 上限 29（30 fps）或 59（60 fps）；不得为被跳过的标签 |
| `frame_index` | 整数，`0` 至当日最后合法帧（含） |
| `start_timecode` / `end_timecode` | 仅 `timecode_span`；约束同 `timecode` |
| `next_day` | 仅 `timecode_span`；可选布尔，缺省 `false`。终点早于起点时须为 `true`，按跨零点计算，跨度不超过一天 |

## 错误响应

输入非法时返回 422（JSON 无法解析返回 400），响应体指出出错字段与稳定错误码，且不携带任何部分换算值：

```json
{"error": {"code": "FRAME_INDEX_OUT_OF_RANGE", "field": "frame_index", "message": "..."}}
```

| 错误码 | 字段 | 含义 |
| --- | --- | --- |
| `INVALID_DIRECTION` | `direction` | 方向取值不支持 |
| `INVALID_RATE` | `rate` | 帧率取值不支持 |
| `MISSING_FIELD` | `timecode` / `frame_index` / `start_timecode` / `end_timecode` | 当前方向必需的字段缺失 |
| `INVALID_TIMECODE_FORMAT` | `timecode` / `start_timecode` / `end_timecode` | 格式或分量越界 |
| `DROPPED_FRAME_LABEL` | `timecode` / `start_timecode` / `end_timecode` | 被丢帧规则跳过的标签 |
| `FRAME_INDEX_OUT_OF_RANGE` | `frame_index` | 负数或越过当日最后合法帧 |
| `END_BEFORE_START` | `end_timecode` | 终点早于起点且未提交 `next_day=true` |
| `MALFORMED_JSON` | — | 请求体不是合法 JSON（HTTP 400） |

## 目录结构

```
cmd/api        API 服务入口
cmd/verify     一次性验收客户端（docker compose 中的 verify 服务）
internal/timecode  丢帧换算核心（纯函数，含全量回环测试）
internal/httpapi   Gin 路由与错误封装（testify 边界向量测试）
```
