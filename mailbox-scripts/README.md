# Mailbox Recovery Scripts

This folder contains three PowerShell scripts designed for administrators to forcefully drain recoverable items from Exchange mailboxes, monitor progress, and restore the mailbox to standard settings.

## Overview

These scripts are used to handle the situation where a mailbox's "Purges" folder has accumulated a large number of items and needs to be emptied. The process involves:

1. **Draining** - Disabling retention policies to allow immediate garbage collection
2. **Monitoring** - Watching the Purges and Deletions folders until they're empty
3. **Restoring** - Re-enabling standard retention settings and policies

## Prerequisites

- Exchange Online PowerShell module installed and configured
- Global Administrator or Exchange Administrator permissions
- PowerShell 5.1 or higher
- Connection to Exchange Online already established

## User Email Input

All three scripts require a target user email address, which can be provided in two ways:

### Method 1: Environment Variable (Recommended for Automation)

Set the `USER_EMAIL` environment variable before running the scripts:

**PowerShell (same session as Exchange Online):**

```powershell
$ProgressPreference = 'SilentlyContinue'
Install-Module ExchangeOnlineManagement -Scope CurrentUser -Force
Connect-ExchangeOnline

$env:USER_EMAIL = "user@contoso.com"
.\01-drain-purged-script.ps1
```

> Run scripts in the **same PowerShell session** you used for `Connect-ExchangeOnline` so the existing session is reused.

### Method 2: Interactive Prompt

If the `USER_EMAIL` environment variable is not set, the script will prompt you to enter the email address:

```
Enter the user email address: user@contoso.com
```

## Scripts

### 01-drain-purged-script.ps1

**Purpose:** Prepare the mailbox for purging by disabling retention policies and triggering garbage collection.

**What it does:**

1. Disables Single Item Recovery
2. Sets retention period to 0 days (immediate deletion)
3. Disables retention policies
4. Resets the Managed Folder Assistant error counter
5. Triggers the Managed Folder Assistant to begin processing

**Usage:**

```powershell
$ProgressPreference = 'SilentlyContinue'
Install-Module ExchangeOnlineManagement -Scope CurrentUser -Force
Connect-ExchangeOnline

# Using environment variable (same session)
$env:USER_EMAIL = "user@contoso.com"
.\01-drain-purged-script.ps1

# Or use interactive prompt (same session)
.\01-drain-purged-script.ps1
```

**Duration:** This script completes within seconds. The actual purging process may take hours or days depending on the volume of items.

**Important Notes:**

- The mailbox will have reduced recoverability during this process
- Monitor the Purges folder using the monitoring script
- Do not interrupt the purging process once started

### 02-monitor-script.ps1

**Purpose:** Monitor the progress of the purging operation in real-time.

**What it displays:**

- **DELETIONS:** Items waiting to be purged from the mailbox
- **PURGES:** Items actively being flushed by the server
- Item count and folder size for each category
- Success message when the dumpster is empty

**Usage:**

```powershell
# Reuse the same session where you ran Connect-ExchangeOnline
$env:USER_EMAIL = "user@contoso.com"
.\02-monitor-script.ps1

# Or use interactive prompt
.\02-monitor-script.ps1
```

**How to stop:** Press `Ctrl+C` to exit the monitoring loop

**Refresh interval:** The display updates every 30 seconds

**Understanding the Output:**

- Purges count decreases as items are permanently deleted
- Deletions count represents items queued for permanent deletion
- When both reach 0, you're ready to run the restore script

### 03-restore-script.ps1

**Purpose:** Restore the mailbox to standard retention settings and re-enable safety features.

**What it does:**

1. Restores the standard 14-day retention window for deleted items
2. Re-enables Single Item Recovery
3. Applies the original retention policy (default: "Default MRM Policy")
4. Triggers the Managed Folder Assistant to resume normal operations

**Usage:**

```powershell
# Reuse the same session where you ran Connect-ExchangeOnline
$env:USER_EMAIL = "user@contoso.com"
.\03-restore-script.ps1

# Or use interactive prompt
.\03-restore-script.ps1
```

**Duration:** This script completes within seconds.

**Important Notes:**

- Verify the correct retention policy before running. The default is "Default MRM Policy"
- To check the original retention policy, use:
  ```powershell
  Get-Mailbox user@contoso.com | Select-Object RetentionPolicy
  ```
- If a custom retention policy was in use, you may need to modify the script

## Step-by-Step Workflow for Windows

1. **Open PowerShell as Administrator:**

   - Right-click on the Start menu and select **Windows PowerShell (Admin)**.

2. **Install the Exchange Online Management Module:**

   - Run the following command to install the module:

   ```powershell
   $ProgressPreference = 'SilentlyContinue'
   Install-Module ExchangeOnlineManagement -Scope CurrentUser -Force
   ```

3. **Connect to Exchange Online:**

   - Use the command below to connect:

   ```powershell
   Connect-ExchangeOnline
   ```

4. **Set the User Email Environment Variable:**

   - Replace `user@contoso.com` with the target email address:

   ```powershell
   $env:USER_EMAIL = "user@contoso.com"
   ```

5. **Run the Drain Script:**

   - Execute the following command to start the draining process:

   ```powershell
   .\01-drain-purged-script.ps1
   ```

6. **Monitor the Progress:**

   - Open a new PowerShell window (keeping the previous session active) and run:

   ```powershell
   .\02-monitor-script.ps1
   ```

   - This will display the status of the purging operation.

7. **Restore the Mailbox:**

   - Once the monitoring indicates that both Purges and Deletions are empty, run:

   ```powershell
   .\03-restore-script.ps1
   ```

8. **Verify Completion:**
   - To ensure everything is completed, you can check the mailbox folder statistics:
   ```powershell
   Get-MailboxFolderStatistics user@contoso.com -FolderScope RecoverableItems
   ```

## Additional Information

- **Important Notes:**
  - Ensure you have the necessary permissions to perform these actions.
  - The mailbox will have reduced recoverability during the draining process.
  - Monitor the Purges folder using the monitoring script.
  - Do not interrupt the purging process once started.

## Troubleshooting

### Script exits with "Error: UserEmail is required"

- Make sure you're entering the email address when prompted, or set the `USER_EMAIL` environment variable

### "Mailbox not found" error

- Verify the email address is correct and spelled correctly
- Ensure you have proper permissions to access the mailbox
- Confirm your Exchange Online connection is active

### Purges folder not decreasing

- The purging process may be slow for large volumes
- Try running the drain script again to trigger another Assistant cycle
- Check if the mailbox has size limitations or throttling in place
- Wait longer (this process can take several days for very large mailboxes)

### Retention policy not applied

- Check what policy was originally applied using:
  ```powershell
  Get-Mailbox user@contoso.com | Select-Object RetentionPolicy
  ```
- Edit the restore script's `$OriginalPolicy` variable with the correct policy name
- Ensure the retention policy exists in your organization

## Safety Considerations

- These scripts **temporarily reduce recovery protection** on the mailbox
- Do not run these scripts on mailboxes that require strict compliance or litigation hold
- Ensure you have backups or archive policies in place for important data
- Only run these scripts during off-hours to minimize user impact
- Use monitoring to verify progress before proceeding to restoration

## Additional Resources

- Exchange Online PowerShell documentation: https://docs.microsoft.com/powershell/exchange/exchange-online-powershell
- Mailbox folder statistics: `Get-MailboxFolderStatistics`
- Managed Folder Assistant info: `Start-ManagedFolderAssistant`
