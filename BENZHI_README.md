# BENZHI_README

## 项目说明

- 项目：11DingKing/goa1047b-t008-05
- 项目用途：A production-grade Go backend for the China–Europe Arctic Express shipping service. It manages peak-season slot reservations, cargo collection with temperature monitoring, loading manifest verification, destination-port delivery confirmation, and exception rescheduling with failure recovery.
- Go 工具链：`golang:1.26`
- 前端工具链：无

## 标准构建、运行和测试命令

进入容器后执行：

```bash
# 编译
cd '/app' && GOTOOLCHAIN=local go build ./...

# 启动
cd '/app' && GOTOOLCHAIN=local go run ./cmd/server

# 测试
cd '/app' && GOTOOLCHAIN=local go test ./...
```

## Docker 构建和进入容器

```bash
chmod +x build_benzhi_docker.sh
./build_benzhi_docker.sh benzhi-task-25-amd64 linux/amd64
./build_benzhi_docker.sh benzhi-task-25-arm64 linux/arm64
docker run -it benzhi-task-25-amd64:latest
docker run -it --platform linux/arm64 benzhi-task-25-arm64:latest
```

## 题目验证命令

1. 预期退出码 1：`go test -timeout=120s ./internal/transport/ -run "TestHTTP_ManifestHoldsTemperatureCargoWithoutAnyReading|TestHTTP_ManifestLoadsTemperatureCargoOnceItReportsInRange|TestHTTP_ManifestHoldsTemperatureCargoOutOfRange|TestTemperatureSweepReportsCargoWithoutAnyReading" -count=1 -v`

## Bug 复现

Bug 现象、触发步骤和完整错误信息见 `BUG_REPRO.md`。
