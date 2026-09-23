# wild-work v2.5.0 — 新增 OpenCodeZen 匿名免费通道（无需账号）

本次新增第 7 个渠道 **OpenCodeZen（`oczen/*`）**：直接使用 OpenCode Zen 的**匿名免费通道**，
**无需注册、无需登录、无需添加账号**，启动即可用。

## 新特性

### OpenCodeZen 匿名免费通道（`oczen/*`）

- 内置官方匿名凭证（`Bearer public`），开箱即用
- 自动从上游同步**免费模型清单**（只暴露免费模型 + `big-pickle`），上游增删免费模型时自动跟随
- 面板「账号管理」固定显示一项 **`[OpenCodeZen] 匿名`**，积分区域显示 **不适用**：
  - 不可添加（无「+ OpenCodeZen」按钮）
  - 不可删除、不可停用
  - 不支持签到、不支持刷新积分
- 支持全部三种接口：Chat Completions、Responses、Anthropic Messages，流式/非流式均可
- 「模型列表和费率」面板中该渠道模型一律标为 **免费**

常用免费模型（以 `/v1/models` 实际返回为准）：

```
oczen/big-pickle
oczen/mimo-v2.6-flash-free
oczen/mimo-v2.5-free
oczen/nemotron-3-ultra-free
oczen/nemotron-3.5-lightning-free
oczen/ling-3.0-flash-fin-free
```

用法示例：

```bash
curl http://127.0.0.1:7863/v1/chat/completions \
  -H "Authorization: Bearer WildWorkAPI" -H "Content-Type: application/json" \
  -d '{"model":"oczen/mimo-v2.6-flash-free","messages":[{"role":"user","content":"你好"}]}'
```

## 说明与限制

- **地域限制**：部分免费模型（如 `muse-spark-*-contributor-free`）对国内直连返回 403
  `RegionError`。这类模型**仍会出现在列表里**，自备代理即可使用。
- **配额**：匿名通道的额度与限流由上游控制。实测未见请求数配额（5 分钟 1159 次、
  64 并发零拒绝），但高并发时响应会变慢（上游排队而非拒绝）。
- **稳定性**：该通道属于上游的非公开承诺接口，上游随时可能调整校验规则或增删免费模型；
  如遇到大量 403 错误，请关注后续版本更新。

## 其它

- 新增 `ErrPassthrough` 错误分类：请求级拒绝（形态/地域问题）只透传原文，
  不冷却账号、不计错误——避免唯一账号被无谓冷却导致整个渠道不可用。

### 用量与积分流水统计（面板新增「用量与流水」区块）

- **双口径独立统计**：token 流水（渠道×模型）与积分流水（账号的入项/消耗/过期）各记各账，
  不做积分↔token 折算
- 面板提供 4 张汇总卡（Token 总量/请求数/积分消耗/积分入项）+ 时间范围切换（今日/7日/30日）
  + Token 用量折线图（echarts，CDN 加载，离线时表格不受影响）+ 模型用量榜 + 账号积分小计
  + 最近 50 条积分流水（绿=入项 / 橙=消耗 / 红=过期作废）
- 积分过期从「猜」变成「对账」：基于上游余额条目快照差分（TraeWork 用 entitlement_id 精确对账）
- 临期阈值可配：`config.json` → `schedule.expiring_threshold_hours`（默认 24，下限 24）
- 数据落盘 `data/ledger/*.jsonl`（按月分段，保留 6 个月），统计仅在打开面板时读取聚合，
  常驻内存近乎为零；流水不含任何 token 凭证
- 注意：统计自本版本上线后开始记录，历史数据无法追溯；首日数据中的「存量额度」是
  各账号现有余额的基线入账
