@echo off
title DP Thumbnail Server
echo.
echo   DP Thumbnail Server v3 - https://adelvo.io/directors-plan
echo   ========================================================
echo   Browser opens automatically. All settings in the web UI.
echo.
where ffmpeg >nul 2>nul
if %ERRORLEVEL% NEQ 0 (
echo   NOTE: ffmpeg not found. Thumbnails will be full-size.
echo   Install for optimized 320x180 thumbs: winget install ffmpeg
echo.
)
echo   Press Ctrl+C to stop.
echo.
dp-thumbnail-server.exe
pause
