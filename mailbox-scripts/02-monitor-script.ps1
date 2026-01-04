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

Write-Host "Monitoring mailbox: $UserEmail" -ForegroundColor Cyan
Write-Host "Press Ctrl+C to stop monitoring." -ForegroundColor Yellow
Write-Host ""

while($true) {
    # Fetch stats for both target folders
    $Folders = Get-MailboxFolderStatistics $UserEmail -FolderScope RecoverableItems | 
               Where-Object {$_.Name -match "Purges|Deletions"}
    
    $PurgeStats = $Folders | Where-Object {$_.Name -eq "Purges"}
    $DeleteStats = $Folders | Where-Object {$_.Name -eq "Deletions"}
    
    Clear-Host
    Write-Host "--- Dumpster Monitoring for $UserEmail ---" -ForegroundColor Cyan
    Write-Host "DELETIONS (Waiting to be purged):"
    Write-Host "  Items: $($DeleteStats.ItemsInFolder)"
    Write-Host "  Size:  $($DeleteStats.FolderAndSubfolderSize)"
    Write-Host "------------------------------------------"
    Write-Host "PURGES (Actively being flushed):" -ForegroundColor Yellow
    Write-Host "  Items: $($PurgeStats.ItemsInFolder)"
    Write-Host "  Size:  $($PurgeStats.FolderAndSubfolderSize)"
    Write-Host "------------------------------------------"
    
    # Simple logic to see if we are close to the finish line
    $TotalCount = [int]$PurgeStats.ItemsInFolder + [int]$DeleteStats.ItemsInFolder
    if ($TotalCount -eq 0) {
        Write-Host "SUCCESS: Dumpster is empty! You can now run the Cleanup Script." -ForegroundColor Green
    } else {
        Write-Host "Status: Purge in progress..." -ForegroundColor Magenta
    }

    # Countdown before the next check
    for ($i = 30; $i -ge 1; $i--) {
        Write-Host ("Next check in {0,2}s `r" -f $i) -NoNewline
        Start-Sleep -Seconds 1
    }
    Write-Host ""
}