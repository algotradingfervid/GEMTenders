#!/bin/bash
# Stop, rebuild, and restart the GEM Tenders server

PORT=28080

# Stop whatever is running on the port
PID=$(lsof -ti :$PORT 2>/dev/null)
if [ -n "$PID" ]; then
    echo "Stopping process $PID on port $PORT..."
    kill $PID
    sleep 1
fi

# Build
echo "Building..."
CGO_ENABLED=1 go build -tags "fts5" -o gemscraper .
if [ $? -ne 0 ]; then
    echo "Build failed"
    exit 1
fi

# Start server in background and open browser
echo "Starting server on :$PORT..."
./gemscraper serve -addr :$PORT &
sleep 2
open "http://localhost:$PORT"
wait
