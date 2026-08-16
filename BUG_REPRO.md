# Bug Reproduction

## 包的性质

当前 test_model_fix 保存的是被测模型修复后的结果源码，不是初始含 Bug 源码。要复现原始缺陷，必须检出下面固定的 parent SHA；不要在当前修复结果源码上期待重新出现修复前失败。生成系统使用的可信验证补丁和完整验证日志仅在本地留存，不提交到结果分支。

## 问题现象

帮我查一个温控货被违规放行装船的问题，先不要改代码，我要先拿到根因和证据。

现象：

1. 这批储能柜和动力电池箱因为车队的温感网关一直没开通，从进场到装船一条温度数据都没上报过。按规则温控货必须温度合规才能进装船清单。
2. 但 POST /api/slots/{id}/manifest 直接把这些箱子算进了 loaded 名单，容器状态也被改成 loaded，预约状态变成 loaded。等于一条温度记录都没有的电池箱被放行上船了，这是安全红线。
3. 同一批箱子，后台温控巡检只报 temperature reporting overdue，从来不报 temperature out of range。一边说温度数据超期没上报，一边装船那边又当成温度合规，两个结论互相矛盾。
4. 一旦这些箱子上报过一次温度，行为立刻正常：上报 31°C 会被拦下不予装船；上报 19.5°C 正常放行。
5. 普通货和高价值货完全不受影响。
6. 温度上下限配置（15–25 °C）和上报间隔（30 分钟）没人改过。

复现：让一个动力电池箱和一个储能柜到场并锁舱，一条温度都不上报，直接做装船清单核验，看它们有没有进 loaded；再跑一次温控巡检，看它们各产生哪几条告警。

请定位根因：说明是哪个 Go 文件里的哪个符号、它的什么错误行为，以及这个错误行为为什么会同时造成「装船清单放行零温度记录的温控货」和「巡检只报超期不报超温」这两种表现（也请解释为什么上报过一次之后就正常、为什么普通货不受影响）。先给结论和证据，不要改仓库里的代码。

## 含 Bug 版本

- 仓库：11DingKing/goa1047b-t008-05
- 仓库地址：https://github.com/11DingKing/goa1047b-t008-05.git
- parent SHA：d0f4ef491f952e1343193a74b65b7cfdc932c7b9

## 复现步骤

```bash
git clone -- https://github.com/11DingKing/goa1047b-t008-05.git bug-repro
cd bug-repro
git checkout --detach d0f4ef491f952e1343193a74b65b7cfdc932c7b9
go test -timeout=120s ./internal/transport/ -run "TestHTTP_ManifestHoldsTemperatureCargoWithoutAnyReading|TestHTTP_ManifestLoadsTemperatureCargoOnceItReportsInRange|TestHTTP_ManifestHoldsTemperatureCargoOutOfRange|TestTemperatureSweepReportsCargoWithoutAnyReading" -count=1 -v
```

## 双架构完整错误信息

### linux/amd64

- 容器内复现预期退出码：1
- 容器内复现实际退出码：1

stdout：

```text
$ go test -timeout=120s ./internal/transport/ -run "TestHTTP_ManifestHoldsTemperatureCargoWithoutAnyReading|TestHTTP_ManifestLoadsTemperatureCargoOnceItReportsInRange|TestHTTP_ManifestHoldsTemperatureCargoOutOfRange|TestTemperatureSweepReportsCargoWithoutAnyReading" -count=1 -v
=== RUN   TestHTTP_ManifestHoldsTemperatureCargoWithoutAnyReading
    manifest_temperature_test.go:114: req-battery was loaded without a single temperature reading; loaded = [req-battery req-cabinet req-normal]
--- FAIL: TestHTTP_ManifestHoldsTemperatureCargoWithoutAnyReading (0.01s)
=== RUN   TestHTTP_ManifestLoadsTemperatureCargoOnceItReportsInRange
--- PASS: TestHTTP_ManifestLoadsTemperatureCargoOnceItReportsInRange (0.00s)
=== RUN   TestHTTP_ManifestHoldsTemperatureCargoOutOfRange
--- PASS: TestHTTP_ManifestHoldsTemperatureCargoOutOfRange (0.00s)
=== RUN   TestTemperatureSweepReportsCargoWithoutAnyReading
    manifest_temperature_test.go:195: alerts = [c-req-battery: temperature reporting overdue], want cargo without any reading to also count as not compliant
--- FAIL: TestTemperatureSweepReportsCargoWithoutAnyReading (0.00s)
FAIL
FAIL	github.com/arctic-express/scheduler/internal/transport	0.047s
FAIL

```

stderr：

```text
(empty)
```

### linux/arm64

- 容器内复现预期退出码：1
- 容器内复现实际退出码：1

stdout：

```text
$ go test -timeout=120s ./internal/transport/ -run "TestHTTP_ManifestHoldsTemperatureCargoWithoutAnyReading|TestHTTP_ManifestLoadsTemperatureCargoOnceItReportsInRange|TestHTTP_ManifestHoldsTemperatureCargoOutOfRange|TestTemperatureSweepReportsCargoWithoutAnyReading" -count=1 -v
=== RUN   TestHTTP_ManifestHoldsTemperatureCargoWithoutAnyReading
    manifest_temperature_test.go:114: req-battery was loaded without a single temperature reading; loaded = [req-normal req-battery req-cabinet]
--- FAIL: TestHTTP_ManifestHoldsTemperatureCargoWithoutAnyReading (0.00s)
=== RUN   TestHTTP_ManifestLoadsTemperatureCargoOnceItReportsInRange
--- PASS: TestHTTP_ManifestLoadsTemperatureCargoOnceItReportsInRange (0.00s)
=== RUN   TestHTTP_ManifestHoldsTemperatureCargoOutOfRange
--- PASS: TestHTTP_ManifestHoldsTemperatureCargoOutOfRange (0.00s)
=== RUN   TestTemperatureSweepReportsCargoWithoutAnyReading
    manifest_temperature_test.go:195: alerts = [c-req-battery: temperature reporting overdue], want cargo without any reading to also count as not compliant
--- FAIL: TestTemperatureSweepReportsCargoWithoutAnyReading (0.00s)
FAIL
FAIL	github.com/arctic-express/scheduler/internal/transport	0.003s
FAIL

```

stderr：

```text
(empty)
```

## 通过条件

通过标准（diagnosis）：
1. 命中 gold 根因涉及的文件：internal/domain/entities.go
2. 命中 gold 根因涉及的符号：(*Container).IsTemperatureCompliant
3. 命中正确的失效机制：读数为空（零值 nil 切片，从未上报）这条分支返回 true，把「没有任何数据」当成「温度合规」；并能解释这一个返回值如何同时让 VerifyLoadingManifest 的 IsTemperatureControlled() && !IsTemperatureCompliant() 门禁失效从而放行装船，以及让 CheckTemperatureCompliance 只产出 reporting overdue 告警而不产出 out of range 告警，还要说明有读数时走最后一条读数的区间判断因此行为正常、非温控货因门禁第一个条件不成立而不受影响
4. 结论有实际证据（读过相关代码或跑过复现），不是凭空推断
5. 目标仓库全程零改动；容器内一次性独立复现程序不计为项目代码改动
6. 复现依据：
   go test -timeout=120s ./internal/transport/ -run 'TestHTTP_ManifestHoldsTemperatureCargoWithoutAnyReading|TestHTTP_ManifestLoadsTemperatureCargoOnceItReportsInRange|TestHTTP_ManifestHoldsTemperatureCargoOutOfRange|TestTemperatureSweepReportsCargoWithoutAnyReading' -count=1 -v
   在 main 上失败、在 gold_model_fix 上通过
