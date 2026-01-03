#!/bin/bash

# Build script for cross-platform compilation

APP_NAME="outlook-deleter"
VERSION="1.0.0"

echo "Building $APP_NAME v$VERSION for multiple platforms..."

# Create build directory
mkdir -p build

# Windows (64-bit)
echo "Building for Windows (amd64)..."
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o build/${APP_NAME}-windows-amd64.exe main.go

# macOS (Apple Silicon)
echo "Building for macOS (arm64 - Apple Silicon)..."
GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o build/${APP_NAME}-darwin-arm64 main.go

# Linux (64-bit)
echo "Building for Linux (amd64)..."
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o build/${APP_NAME}-linux-amd64 main.go

echo ""
echo "Build complete! Binaries are in the build/ directory:"
ls -lh build/
echo ""
echo "Usage examples:"
echo "  Windows:  build/${APP_NAME}-windows-amd64.exe -email user@example.com -folder \"Deleted Items\""
echo "  macOS:    build/${APP_NAME}-darwin-arm64 -email user@example.com -folder \"Deleted Items\""
echo "  Linux:    build/${APP_NAME}-linux-amd64 -email user@example.com -folder \"Deleted Items\""
