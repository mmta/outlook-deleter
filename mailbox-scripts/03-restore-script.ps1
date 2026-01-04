# Read from environment variable or prompt user
if ([string]::IsNullOrEmpty($env:USER_EMAIL)) {
    $UserEmail = Read-Host "Enter the user email address"
} else {
    $UserEmail = $env:USER_EMAIL
}

if ([string]::IsNullOrEmpty($UserEmail)) {
    Write-Host "Error: UserEmail is required." -ForegroundColor Red
    exit 1
}

Write-Host "Restoring mailbox: $UserEmail" -ForegroundColor Cyan
Write-Host ""

# 1. Restore standard 14-day retention window
Write-Host "Restoring standard 14-day safety window..." -ForegroundColor Cyan
Set-Mailbox -Identity $UserEmail -RetainDeletedItemsFor 14 -SingleItemRecoveryEnabled $true

# 2. Re-apply the original Retention Policy (Replace 'Default MRM Policy' with yours)
# Check original via: Get-Mailbox $UserEmail | Select-Object RetentionPolicy
$OriginalPolicy = "Default MRM Policy" 
Set-Mailbox -Identity $UserEmail -RetentionPolicy $OriginalPolicy

# 3. Final nudge to the server to resume normal operations
Start-ManagedFolderAssistant -Identity $UserEmail

Write-Host "Cleanup complete. Mailbox is back to standard safety settings." -ForegroundColor Green