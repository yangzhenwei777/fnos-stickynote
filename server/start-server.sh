#!/bin/bash

# 停止13389端口的服务
echo "Stopping any existing service on port 13389..."
PID=$(lsof -t -i :13389)
if [ -n "$PID" ]; then
  kill $PID
  echo "Stopped existing service (PID: $PID)"
  sleep 1
fi

# 启动服务
echo "Starting Notepad server..."
go run main.go
