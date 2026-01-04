# 1. Define the target
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

Write-Host "Target mailbox: $UserEmail" -ForegroundColor Cyan
Write-Host ""

# 2. Block processing to clear the execution queue
Write-Host "Step 1: Disabling ELC Processing to clear backend hang..."
Set-Mailbox -Identity $UserEmail -ElcProcessingDisabled $true
Start-Sleep -Seconds 5

# 3. Toggle Retention to 'Dirty' the mailbox record
# This forces the database to re-calculate the expiry on the next pass
Write-Host "Step 2: Performing Hard Reset on Retention Timers..." -ForegroundColor Yellow
Set-Mailbox -Identity $UserEmail -RetainDeletedItemsFor 1 -SingleItemRecoveryEnabled $false -RetentionPolicy $null
Start-Sleep -Seconds 3
Set-Mailbox -Identity $UserEmail -RetainDeletedItemsFor 0
Start-Sleep -Seconds 3

# 3. Step 3: Removing potential Delay/Release holds (only if they exist)
Write-Host "Step 3: Checking for hidden Delay/Release holds..." -ForegroundColor Gray
$mbx = Get-Mailbox $UserEmail
if ($null -ne $mbx.DelayHoldEnabled) {
    Set-Mailbox -Identity $UserEmail -DelayHoldEnabled $false -ErrorAction SilentlyContinue
}
if ($null -ne $mbx.DelayReleaseHoldEnabled) {
    Set-Mailbox -Identity $UserEmail -DelayReleaseHoldEnabled $false -ErrorAction SilentlyContinue
}

# 5. Re-enable and Force High-Priority Crawl
Write-Host "Step 4: Re-enabling Assistant and triggering Full Crawl..." -ForegroundColor Green
Set-Mailbox -Identity $UserEmail -ElcProcessingDisabled $false
Start-ManagedFolderAssistant -Identity $UserEmail -FullCrawl

Write-Host "Done. The database index has been reset. Monitor stats with the -Refresh flag." -ForegroundColor Cyan