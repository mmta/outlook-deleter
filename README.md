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
# Soft delete (move to Recoverable Items)
./outlook-deleter delete -folder "Deleted Items"
./outlook-deleter delete -folder "SOC/Support" -workers 2

# Permanent delete (bypass recovery)
./outlook-deleter delete -folder "Junk Email" -permanent
```

### Using environment variables

```bash
TARGET_FOLDER="Deleted Items" MAX_WORKERS=3 ./outlook-deleter delete
PERMANENT_DELETE=true ./outlook-deleter delete -folder "Spam"
```

## Configuration

### Command-line Options

| Flag/Env Var                      | Description                          | Required         | Default |
| --------------------------------- | ------------------------------------ | ---------------- | ------- |
| `-folder` / `TARGET_FOLDER`       | Target folder path                   | Yes (for delete) | -       |
| `-workers` / `MAX_WORKERS`        | Parallel workers (1-10)              | No               | 5       |
| `-permanent` / `PERMANENT_DELETE` | Permanently delete (bypass recovery) | No               | false   |

Flags take precedence over environment variables.

**Note**: By default, messages are soft-deleted to the hidden "Recoverable Items" folder (not visible "Deleted Items"). They can be recovered via "Recover items deleted from this folder" in Outlook. Use `-permanent` flag to permanently delete and bypass recovery.

## Authentication

- **First run**: Browser-based OAuth2 authentication (or device code flow in WSL)
- **WSL**: Automatic device code flow for better compatibility
- **Token caching**: Access tokens are cached locally for 1 hour
  - **Linux/macOS**: `~/.cache/outlook-deleter/token.json`
  - **Windows**: `%LOCALAPPDATA%\outlook-deleter\token.json`
  - Tokens are automatically reused on subsequent runs within 1 hour

## Limitations

- **Soft delete by default**: Messages are soft-deleted to **Recoverable Items** (hidden folder) by default, not the visible "Deleted Items" folder. Items can be recovered via "Recover items deleted from this folder" in Outlook. Use the `-permanent` flag to permanently delete and bypass recovery. ([Delete API](https://learn.microsoft.com/en-us/graph/api/message-delete) vs [PermanentDelete API](https://learn.microsoft.com/en-us/graph/api/message-permanentdelete))
- **No date-based filtering**: Deletes all messages in the target folder (no way to filter by date range)
- **No sender/subject filtering**: Cannot filter messages by sender, subject, or other criteria
- **No size-based filtering**: Cannot filter by message size
- **Folder-level only**: Operates on entire folders, not individual messages or subsets
- **No dry-run mode**: Use with caution - soft deletes move to "Deleted Items", permanent deletes are immediate and irreversible
- **Rate limiting**: Microsoft Graph API enforces rate limits; tool will retry but large deletions take time
- **Primary mailbox only**: In-place archive mailboxes (separate archive stores) are not supported - only the primary mailbox is accessible

## Troubleshooting

### Permission Denied (403)

**Error**: `"message":"Insufficient privileges to complete the operation."`

**Solution**: Ensure `Mail.ReadWrite` permission is configured in your Azure app registration and admin consent has been granted.

### Token Expired (401)

**Error**: `"code":"InvalidAuthenticationToken","message":"Lifetime validation failed, the token is expired."`

**Solution**: The tool automatically refreshes tokens, but if this persists:

1. Delete the cached token file:
   - Linux/macOS: `rm ~/.cache/outlook-deleter/token.json`
   - Windows: `del %LOCALAPPDATA%\outlook-deleter\token.json`
2. Run the tool again and re-authenticate

### Rate Limiting (429)

**Error**: `status 429, body: {"error":{"code":"ThrottlingNotAllowed",...}}`

**Solution**: Microsoft Graph enforces rate limits. The tool automatically retries with exponential backoff. To speed up:

- Reduce the number of parallel workers: `-workers 2` or `-workers 1`
- Run during off-peak hours
- Break large deletions into smaller batches by folder
- If you are simultaneously performing heavy Outlook.com actions (e.g., emptying Recoverable Items or other bulk mailbox work), shared mailbox workload throttles can trigger additional 429s unrelated to Graph rate limit. Pause the other activity and retry once the mailbox load subsides.

### Folder Not Found

**Error**: `Error: folder not found`

**Solution**:

1. Run `./outlook-deleter list` to see available folders
2. Use the exact folder path (case-sensitive)
3. Use forward slashes for nested folders: `Folder/Subfolder`

## Using a Different Azure Organization

By default, the tool uses _my_ organization credentials. To use a different Azure organization, you need to:

1. **Set up your own Azure App Registration** (see [Azure App Setup](#azure-app-setup-for-custom-organizations) section)
2. **Get your Client ID and Tenant ID** from your app registration
3. **Configure the tool** with your credentials:

```bash
export OUTLOOK_CLIENT_ID=your-client-id
export OUTLOOK_TENANT_ID=your-tenant-id
./outlook-deleter list
```

Or in one command:

```bash
OUTLOOK_CLIENT_ID=xxx OUTLOOK_TENANT_ID=yyy ./outlook-deleter delete -folder "Inbox"
```

## Azure App Setup (for Custom Organizations)

If you want to use this tool with your own Azure organization instead of the default:

### 1. Create an Azure App Registration

1. Go to [Azure Portal](https://portal.azure.com)
2. Navigate to **Azure Active Directory** → **App registrations**
3. Click **New registration**
4. Enter a name (e.g., "Outlook Deleter")
5. Select **Accounts in this organizational directory only**
6. Click **Register**

### 2. Configure API Permissions

1. In your app registration, go to **API permissions**
2. Click **Add a permission**
3. Select **Microsoft Graph**
4. Choose **Delegated permissions**
5. Search for and select **Mail.ReadWrite**
6. Click **Add permissions**
7. If you have admin rights, click **Grant admin consent for [Your Organization]**

### 3. Get Your Credentials

1. In your app registration, go to **Overview**
2. Copy the **Application (client) ID**
3. Copy the **Directory (tenant) ID**

### 4. Use with the Tool

```bash
export OUTLOOK_CLIENT_ID=<your-client-id>
export OUTLOOK_TENANT_ID=<your-tenant-id>
./outlook-deleter list
```

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
