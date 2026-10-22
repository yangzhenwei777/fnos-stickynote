#!/bin/bash

# 停止13389端口的服务
echo "Stopping service on port 13389..."

# 查找占用13389端口的进程ID
PID=$(lsof -t -i :13389)

if [ -z "$PID" ]; then
  echo "No service found on port 13389"
else
  echo "Found process $PID on port 13389"
  kill $PID
  echo "Service stopped successfully"
fi
