#!/bin/bash
# Build for Windows (run on Mac/Linux)
echo "Building DP Thumbnail Server for Windows..."
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o dp-thumbnail-server.exe .
echo "Done: dp-thumbnail-server.exe"
echo ""
echo "Deploy to vMix machine:"
echo "  1. Copy dp-thumbnail-server.exe + start.bat"
echo "  2. Make sure ffmpeg.exe is in PATH or same folder"
echo "  3. Double-click start.bat"
