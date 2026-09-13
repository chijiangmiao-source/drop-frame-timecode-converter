# dropframe-api

广播级丢帧时间码（Drop-Frame Timecode）与真实帧序号的纯后端换算服务。解决素材清单把丢帧时间码当作连续计数导致的定位偏差：节目越长，时间码标签与真实帧序号的差值越大，必须按丢帧规则换算才能精确定位。

- Go 1.25 + Gin，无外部服务依赖
- 支持 `30000/1001`（名义 30 fps，每分钟丢 2 个标签）与 `60000/1001`（名义 60 fps，每分钟丢 4 个标签）
- 正向：时间码 → 从当天 `00:00:00;00` 起、首帧为 0 的整数帧序号
- 反向：帧序号 → 当日唯一合法时间码标签
- 跨度：两个时间码 → 实际帧间隔（不含起点帧），可选跨零点续算，避免客户端各自处理日期翻转
- 偏移：时间码 + 带符号整数帧数 → 目标时间码与日期位移（前一日 -1、当日 0、次日 1），结果只落在相邻自然日，剪辑点前移或跨午夜后移均由服务端归一化
- 帧率迁移：源帧率 + 目标帧率 + 时间码 → 目标帧率下的对应时间码，供混用两种帧率的工程间迁移定位点；按帧序号的二倍关系精确映射，60 fps 侧落在 30 fps 侧半帧位置的定位点返回 422，不用浮点秒数换算
- 时间线审计：帧率 + 按播出顺序排列的片段列表（标识、入点、出点）→ 一次性审计出空隙与重叠。按真实帧序号比较相邻片段，生成审计编号、通过/未通过状态与带前后片段标识和帧数差的有序问题清单；报告保存在进程内仓库中，可按审计编号反复读取供后续质检使用
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

`verify` 服务等待 API 健康后执行验收：边界向量、丢帧标签非法性、1440 次分钟连续衔接（含十分钟边界）、时间码跨度（同日、跨午夜、零跨度与未授权跨日拒绝）、时间码偏移（两种帧率的零偏移、十分钟边界前移、末帧跨次日首帧与越界拒绝）、帧率迁移（同帧率直返、双向十分钟边界映射、30→60→30 往返一致与半帧位置拒绝）、时间线审计（连续片段通过并按编号读回报告、同时识别空隙与重叠、非法片段拒绝且不落库、未知编号返回 404）、错误响应格式、抽样回环可逆性，全部通过则以退出码 0 结束，否则非 0。

本地开发（需要 Go 1.25）：

```bash
go test ./...        # 单元测试（含当日全量帧序号回环验证）
go run ./cmd/api     # 监听 :8080，可用 LISTEN_ADDR 覆盖
```

## 请求示例

`POST /api/v1/convert`，请求体包含 `direction`、`rate`（`timecode_retime` 为 `source_rate` + `target_rate`），以及按方向选择的 `timecode`、`frame_index`、`start_timecode` + `end_timecode`（可选 `next_day`）或 `timecode` + 整数 `frame_offset`。

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

偏移换算（时间码 + 带符号整数帧数 → 目标时间码与日期位移）。调整片段入点时，直接提交需要移动的帧数即可，跨日回绕由服务端处理：`day_offset` 为 `-1`、`0`、`1` 分别表示结果落在前一日、当日与次日，`frame_offset=0` 时原样返回标签与 `0`。

剪辑点前移一帧（30 fps 下从十分钟边界退回上一分钟末帧，仍在当日）：

```bash
curl -s -X POST http://localhost:8080/api/v1/convert \
  -H 'Content-Type: application/json' \
  -d '{"direction":"timecode_offset","rate":"30000/1001","timecode":"00:10:00;00","frame_offset":-1}'
```

```json
{"timecode": "00:09:59;29", "day_offset": 0}
```

跨午夜后移（当日最后一帧后移一帧即为次日首帧，`day_offset=1`；同理首帧前移一帧得前一日末帧与 `-1`）：

```bash
curl -s -X POST http://localhost:8080/api/v1/convert \
  -H 'Content-Type: application/json' \
  -d '{"direction":"timecode_offset","rate":"30000/1001","timecode":"23:59:59;29","frame_offset":1}'
```

```json
{"timecode": "00:00:00;00", "day_offset": 1}
```

结果仅可落在相邻自然日：偏移按当日总帧数（30 fps 为 2589408 帧、60 fps 为 5178816 帧）归一化，例如从当日末帧后移一整天恰好到次日末帧仍合法，再多一帧即越过次日，返回 422 `OFFSET_OUT_OF_RANGE`（指向 `frame_offset`）且不携带目标值。

帧率迁移（源帧率 + 目标帧率 + 时间码 → 目标帧率时间码）。在混用两种帧率的工程间迁移定位点时，服务端先把源标签解析为帧序号，再按两种帧率的二倍关系映射：30 fps → 60 fps 将序号乘二，60 fps → 30 fps 仅在序号为偶数时除二，因此往返定位完全一致；源与目标帧率相同时原样返回标签。

30 fps 定位点迁入 60 fps 工程（十分钟边界前一帧 `00:09:59;29` 映射为 `00:09:59;58`）：

```bash
curl -s -X POST http://localhost:8080/api/v1/convert \
  -H 'Content-Type: application/json' \
  -d '{"direction":"timecode_retime","source_rate":"30000/1001","target_rate":"60000/1001","timecode":"00:09:59;29"}'
```

```json
{"timecode": "00:09:59;58"}
```

反向迁移时，60 fps 侧落在 30 fps 侧半帧位置的定位点（帧序号为奇数）没有精确对应标签，返回 422 `TIMECODE_NOT_ALIGNED`（指向 `timecode`）且不携带目标值：

```bash
curl -s -X POST http://localhost:8080/api/v1/convert \
  -H 'Content-Type: application/json' \
  -d '{"direction":"timecode_retime","source_rate":"60000/1001","target_rate":"30000/1001","timecode":"00:10:00;01"}'
```

```json
{"error": {"code": "TIMECODE_NOT_ALIGNED", "field": "timecode", "message": "timecode \"00:10:00;01\" at rate 60000/1001 falls on a half-frame position of rate 30000/1001 and has no exact target label"}}
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

## 时间线审计

制作人员导入按播出顺序排列的片段后，提交帧率与片段列表（每段含 `id`、`in`、`out` 三个字段，入点出点均为含本帧的时间码标签），服务端逐段校验标签合法性，再按真实帧序号比较相邻片段：下一段入点恰为上一段出点的下一帧视为连续，否则按差值报告空隙（缺失帧数）或重叠（共同占用的帧数）。

`POST /api/v1/audits` 创建审计，返回 201 与完整报告；报告同时存入进程内仓库，可用 `GET /api/v1/audits/{audit_id}` 反复读取（200），供后续质检核对。审计编号单调递增（`aud-000001` 起），片段不足两个、标识为空或重复、出点早于入点、时间码非法时整体拒绝（422）且不保存任何报告，失败响应不夹带审计结果；未知编号返回 404 `AUDIT_NOT_FOUND`。

连续时间线（跨丢帧分钟边界时标签跳变但帧序号连续，仍判定为连续）：

```bash
curl -s -X POST http://localhost:8080/api/v1/audits \
  -H 'Content-Type: application/json' \
  -d '{"rate":"30000/1001","segments":[
        {"id":"seg-1","in":"00:00:00;00","out":"00:00:59;29"},
        {"id":"seg-2","in":"00:01:00;02","out":"00:09:59;29"},
        {"id":"seg-3","in":"00:10:00;00","out":"00:10:00;00"}]}'
```

```json
{"audit_id": "aud-000001", "rate": "30000/1001", "status": "passed", "segment_count": 3, "issues": []}
```

同时存在空隙与重叠的时间线（问题按播出顺序排列，注明前后片段标识与帧数差）：

```bash
curl -s -X POST http://localhost:8080/api/v1/audits \
  -H 'Content-Type: application/json' \
  -d '{"rate":"30000/1001","segments":[
        {"id":"seg-a","in":"00:00:01;00","out":"00:00:10;00"},
        {"id":"seg-b","in":"00:00:12;00","out":"00:00:20;00"},
        {"id":"seg-c","in":"00:00:19;20","out":"00:00:30;00"}]}'
```

```json
{"audit_id": "aud-000002", "rate": "30000/1001", "status": "failed", "segment_count": 3,
 "issues": [
   {"kind": "gap", "previous_segment": "seg-a", "next_segment": "seg-b", "frames": 59},
   {"kind": "overlap", "previous_segment": "seg-b", "next_segment": "seg-c", "frames": 11}
 ]}
```

按编号读取已创建的报告：

```bash
curl -s http://localhost:8080/api/v1/audits/aud-000002
```

未知编号：

```json
{"error": {"code": "AUDIT_NOT_FOUND", "field": "audit_id", "message": "audit report \"aud-999999\" does not exist"}}
```

## 字段约束

| 字段 | 约束 |
| --- | --- |
| `direction` | `timecode_to_frame`、`frame_to_timecode`、`timecode_span`、`timecode_offset` 或 `timecode_retime` |
| `rate` | `30000/1001` 或 `60000/1001`（`timecode_retime` 不使用） |
| `source_rate` / `target_rate` | 仅 `timecode_retime`；约束同 `rate`，二者相同则原样返回标签 |
| `timecode` | 严格 `HH:MM:SS;FF`；HH 00-23，MM/SS 00-59；FF 上限 29（30 fps）或 59（60 fps）；不得为被跳过的标签 |
| `frame_index` | 整数，`0` 至当日最后合法帧（含） |
| `frame_offset` | 仅 `timecode_offset`；带符号整数帧数。结果归一化到前一日、当日或次日；越过相邻自然日返回 422 `OFFSET_OUT_OF_RANGE` |
| `start_timecode` / `end_timecode` | 仅 `timecode_span`；约束同 `timecode` |
| `next_day` | 仅 `timecode_span`；可选布尔，缺省 `false`。终点早于起点时须为 `true`，按跨零点计算，跨度不超过一天 |
| `segments` | 仅审计接口；按播出顺序排列的片段数组，至少两段。每段含 `id`（非空且全列表唯一）、`in`、`out`（约束同 `timecode`，含本帧；出点不得早于入点） |

## 错误响应

输入非法时返回 422（JSON 无法解析返回 400，查询不存在的审计报告返回 404），响应体指出出错字段与稳定错误码，且不携带任何部分换算值或审计结果：

```json
{"error": {"code": "FRAME_INDEX_OUT_OF_RANGE", "field": "frame_index", "message": "..."}}
```

| 错误码 | 字段 | 含义 |
| --- | --- | --- |
| `INVALID_DIRECTION` | `direction` | 方向取值不支持 |
| `INVALID_RATE` | `rate` / `source_rate` / `target_rate` | 帧率取值不支持 |
| `MISSING_FIELD` | `timecode` / `frame_index` / `frame_offset` / `start_timecode` / `end_timecode` | 当前方向必需的字段缺失 |
| `INVALID_TIMECODE_FORMAT` | `timecode` / `start_timecode` / `end_timecode` | 格式或分量越界 |
| `DROPPED_FRAME_LABEL` | `timecode` / `start_timecode` / `end_timecode` | 被丢帧规则跳过的标签 |
| `FRAME_INDEX_OUT_OF_RANGE` | `frame_index` | 负数或越过当日最后合法帧 |
| `OFFSET_OUT_OF_RANGE` | `frame_offset` | 偏移后越过前一日或次日（结果只能落在相邻自然日） |
| `END_BEFORE_START` | `end_timecode` / `segments[i].out` | 终点早于起点且未提交 `next_day=true`，或审计片段的出点早于入点 |
| `TIMECODE_NOT_ALIGNED` | `timecode` | 60 fps 定位点落在 30 fps 侧的半帧位置，无精确目标标签 |
| `TOO_FEW_SEGMENTS` | `segments` | 审计片段列表少于两段 |
| `DUPLICATE_SEGMENT_ID` | `segments[i].id` | 片段标识在列表中重复（或为空时按 `MISSING_FIELD` 处理） |
| `AUDIT_NOT_FOUND` | `audit_id` | 按编号查询的审计报告不存在（HTTP 404） |
| `AMBIGUOUS_FIELD` | 冲突字段 | 同一字段重复出现且取值不同，请求含义不唯一。大小写不同但映射到同一字段的拼写（如 `source_rate` 与 `Source_Rate`）视为同一字段 |
| `MALFORMED_JSON` | — | 请求体不是单一、合法 JSON 对象（HTTP 400） |

## 目录结构

```
cmd/api        API 服务入口
cmd/verify     一次性验收客户端（docker compose 中的 verify 服务）
internal/timecode  丢帧换算核心（纯函数，含全量回环测试）
internal/audit     时间线审计领域模块（复用丢帧换算规则）与进程内报告仓库
internal/httpapi   Gin 路由与错误封装（testify 边界向量测试）
```
