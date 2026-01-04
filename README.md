# Outlook Email Deleter

A command-line tool for deleting emails from Microsoft Outlook folders using Microsoft Graph API.

## Features

- OAuth2 authentication using MSAL for Go
- Recursive folder listing with message counts
- Parallel email deletion with configurable workers
- Automatic retry with exponential backoff for rate limiting
- WSL support

## Prerequisites

- Go 1.22+ (for building from source)
- Azure AD app registration with Mail.ReadWrite permission
- Valid Microsoft 365 account

## Installation

### Pre-built Binaries

Download the latest binaries from [GitHub Releases](https://github.com/mmta/outlook-deleter/releases):

- Windows: `outlook-deleter-windows-amd64.exe`
- macOS (Apple Silicon): `outlook-deleter-darwin-arm64`
- Linux: `outlook-deleter-linux-amd64`

### Build from Source

```bash
go mod download
go build -o outlook-deleter main.go
```

## Usage

### List all folders

```bash
./outlook-deleter list
```

### Delete emails from a folder

```bash
./outlook-deleter delete -folder "Deleted Items"
./outlook-deleter delete -folder "SOC/Support" -workers 2
```

### Using environment variables

```bash
TARGET_FOLDER="Deleted Items" MAX_WORKERS=3 ./outlook-deleter delete
```

## Configuration

| Flag/Env Var                | Description             | Required         | Default |
| --------------------------- | ----------------------- | ---------------- | ------- |
| `-folder` / `TARGET_FOLDER` | Target folder path      | Yes (for delete) | -       |
| `-workers` / `MAX_WORKERS`  | Parallel workers (1-10) | No               | 5       |

Flags take precedence over environment variables.

## Authentication

- **First run**: Browser-based OAuth2 authentication (or device code flow in WSL)
- **WSL**: Automatic device code flow for better compatibility
- **Token caching**: Access tokens are cached locally for 1 hour
  - **Linux/macOS**: `~/.cache/outlook-deleter/token.json`
  - **Windows**: `%LOCALAPPDATA%\outlook-deleter\token.json`
  - Tokens are automatically reused on subsequent runs within 1 hour

## Releasing a New Version

To create a new release:

1. Update the version in the `VERSION` file:

   ```bash
   echo "1.0.1" > VERSION
   ```

2. Commit and push to main:

   ```bash
   git add VERSION
   git commit -m "Release v1.0.1"
   git push origin main
   ```

3. GitHub Actions will automatically:
   - Build binaries for Windows, macOS, and Linux
   - Create a GitHub release tagged as `v1.0.1`
   - Attach all binaries to the release
