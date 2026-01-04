package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
)

// --- Configuration ---
const (
	ClientID = "51b31bec-d0d8-4939-8b58-e53f56b9e373"
	TenantID = "79ba327a-98fe-4f8e-83ac-e9596bac64dc"

	GraphAPI   = "https://graph.microsoft.com/v1.0"
	MaxRetries = 3 // Max retries for rate-limited requests
	RetryDelay = 1 * time.Second
)

// Runtime configuration (from flags or env vars)
var (
	targetFolder string
	maxWorkers   int
)

// Global token state for automatic refresh
var (
	currentToken    string
	tokenMutex      sync.Mutex
	msalClient      public.Client
	lastAuthAccount public.Account
	usedCachedToken bool // Track if we used cache at startup
)

var (
	authority = fmt.Sprintf("https://login.microsoftonline.com/%s", TenantID)
	scopes    = []string{"https://graph.microsoft.com/Mail.ReadWrite"}
)

// API Response structures
type Folder struct {
	ID              string `json:"id"`
	DisplayName     string `json:"displayName"`
	TotalItemCount  int    `json:"totalItemCount"`
	UnreadItemCount int    `json:"unreadItemCount"`
}

type FoldersResponse struct {
	Value    []Folder `json:"value"`
	NextLink string   `json:"@odata.nextLink"`
}

type Message struct {
	ID string `json:"id"`
}

type MessagesResponse struct {
	Value    []Message `json:"value"`
	NextLink string    `json:"@odata.nextLink"`
}

// TokenCache represents a cached access token
type TokenCache struct {
	AccessToken string    `json:"accessToken"`
	ExpiresAt   time.Time `json:"expiresAt"`
	AccountID   string    `json:"accountId"`
}

func getTokenCachePath() (string, error) {
	// Use ~/.cache/outlook-deleter/token.json
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	cacheDir := filepath.Join(home, ".cache", "outlook-deleter")
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "token.json"), nil
}

func getCachedToken() (string, error) {
	cachePath, err := getTokenCachePath()
	if err != nil {
		return "", nil // Silently fail, fall back to auth
	}

	data, err := os.ReadFile(cachePath)
	if err != nil {
		return "", nil // File doesn't exist or can't be read
	}

	var cache TokenCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return "", nil // Invalid format, fall back to auth
	}

	// Check if token is still valid (with 5 min buffer)
	if time.Now().Add(5 * time.Minute).Before(cache.ExpiresAt) {
		return cache.AccessToken, nil
	}

	return "", nil // Token expired
}

func cacheToken(accessToken, accountID string, expiresIn int64) error {
	cachePath, err := getTokenCachePath()
	if err != nil {
		return nil // Silently fail
	}

	cache := TokenCache{
		AccessToken: accessToken,
		ExpiresAt:   time.Now().Add(time.Duration(expiresIn) * time.Second),
		AccountID:   accountID,
	}

	data, err := json.Marshal(cache)
	if err != nil {
		return nil // Silently fail
	}

	return os.WriteFile(cachePath, data, 0600)
}

func refreshAccessToken() (string, error) {
	tokenMutex.Lock()
	defer tokenMutex.Unlock()

	ctx := context.Background()

	// Try silent refresh with the last authenticated account
	if lastAuthAccount.HomeAccountID != "" {
		result, err := msalClient.AcquireTokenSilent(ctx, scopes, public.WithSilentAccount(lastAuthAccount))
		if err == nil {
			currentToken = result.AccessToken
			expiresIn := result.ExpiresOn.Unix() - time.Now().Unix()
			cacheToken(result.AccessToken, result.Account.HomeAccountID, expiresIn)
			fmt.Println("\n✓ Token automatically refreshed")
			return result.AccessToken, nil
		}
		fmt.Printf("\nWarning: Token refresh failed (%v)\n", err)
	}

	return "", fmt.Errorf("unable to refresh token")
}

// isWSL detects if we're running in Windows Subsystem for Linux
func isWSL() bool {
	if runtime.GOOS != "linux" {
		return false
	}

	// Check for WSL indicators
	if _, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop"); err == nil {
		return true
	}

	// Fallback: check /proc/version for "microsoft" or "WSL"
	data, err := os.ReadFile("/proc/version")
	if err == nil {
		version := strings.ToLower(string(data))
		return strings.Contains(version, "microsoft") || strings.Contains(version, "wsl")
	}

	return false
}

func getAccessToken() (string, error) {
	var err error
	msalClient, err = public.New(ClientID, public.WithAuthority(authority))
	if err != nil {
		return "", fmt.Errorf("failed to create public client: %w", err)
	}

	ctx := context.Background()

	// Try to get from local cache first
	cachePath, _ := getTokenCachePath()
	if cachePath != "" {
		data, err := os.ReadFile(cachePath)
		if err == nil {
			var cache TokenCache
			if err := json.Unmarshal(data, &cache); err == nil {
				// Check if access token is still valid (with 5 min buffer)
				if time.Now().Add(5 * time.Minute).Before(cache.ExpiresAt) {
					fmt.Println("✓ Using cached access token")
					usedCachedToken = true

					// Try to load the account for future refreshes
					accounts, _ := msalClient.Accounts(ctx)
					for _, acc := range accounts {
						if acc.HomeAccountID == cache.AccountID {
							lastAuthAccount = acc
							break
						}
					}

					currentToken = cache.AccessToken
					return cache.AccessToken, nil
				}
			}
		}
	}

	// Try to get token from MSAL cache
	accounts, err := msalClient.Accounts(ctx)
	if err != nil {
		fmt.Printf("Cache check: no accounts found (%v)\n", err)
	} else if len(accounts) > 0 {
		fmt.Printf("Cache check: found account %s\n", accounts[0].PreferredUsername)
		result, err := msalClient.AcquireTokenSilent(ctx, scopes, public.WithSilentAccount(accounts[0]))
		if err == nil {
			lastAuthAccount = accounts[0]
			usedCachedToken = true
			cacheToken(result.AccessToken, result.Account.HomeAccountID, result.ExpiresOn.Unix()-time.Now().Unix())
			fmt.Println("✓ Using MSAL cached token")
			currentToken = result.AccessToken
			return result.AccessToken, nil
		}
		fmt.Printf("Cache check: silent token acquisition failed (%v), will re-authenticate\n", err)
	} else {
		fmt.Println("Cache check: no cached accounts found")
	}

	// No cached token, choose authentication method based on environment
	fmt.Println("Prompting for authentication...")

	// Use device code flow for WSL (more reliable), interactive for others
	var result public.AuthResult
	if isWSL() {
		fmt.Println("WSL detected, using device code flow...")
		fmt.Println("Please authenticate using the device code flow.")
		fmt.Println()

		deviceCode, err := msalClient.AcquireTokenByDeviceCode(ctx, scopes)
		if err != nil {
			return "", fmt.Errorf("failed to initiate device code flow: %w", err)
		}

		// Display the code and URL to the user
		fmt.Println(deviceCode.Result.Message)

		// Wait for the user to authenticate
		result, err = deviceCode.AuthenticationResult(ctx)
		if err != nil {
			return "", fmt.Errorf("device code authentication failed: %w", err)
		}
	} else {
		// Native interactive browser flow for non-WSL environments
		result, err = msalClient.AcquireTokenInteractive(ctx, scopes)
		if err != nil {
			return "", fmt.Errorf("interactive authentication failed: %w", err)
		}
	}

	// Store the authenticated account for future refreshes
	lastAuthAccount = result.Account
	expiresIn := result.ExpiresOn.Unix() - time.Now().Unix()
	cacheToken(result.AccessToken, result.Account.HomeAccountID, expiresIn)
	currentToken = result.AccessToken

	return result.AccessToken, nil
}

func makeRequest(method, url, token string) (*http.Response, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Prefer", "outlook.body-content-type=\"text\"")

	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			ResponseHeaderTimeout: 30 * time.Second,
			DisableKeepAlives:     false,
		},
	}
	return client.Do(req)
}

func displayFoldersRecursive(token, folderID, indent string) error {
	url := fmt.Sprintf("%s/me/mailFolders?$top=250", GraphAPI)
	if folderID != "" {
		url = fmt.Sprintf("%s/me/mailFolders/%s/childFolders?$top=250", GraphAPI, folderID)
	}

	for url != "" {
		resp, err := makeRequest("GET", url, token)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("failed to get folders: status %d", resp.StatusCode)
		}

		var foldersResp FoldersResponse
		if err := json.NewDecoder(resp.Body).Decode(&foldersResp); err != nil {
			return err
		}

		for _, folder := range foldersResp.Value {
			fmt.Printf("%s%-40s | Total: %8d | Unread: %8d\n",
				indent, folder.DisplayName, folder.TotalItemCount, folder.UnreadItemCount)
			// Recursively display subfolders
			if err := displayFoldersRecursive(token, folder.ID, indent+"  "); err != nil {
				return err
			}
		}

		url = foldersResp.NextLink
	}

	return nil
}

func findFolderRecursive(token, targetName, folderID, parentPath string) (*Folder, string, error) {
	url := fmt.Sprintf("%s/me/mailFolders?$top=250", GraphAPI)
	if folderID != "" {
		url = fmt.Sprintf("%s/me/mailFolders/%s/childFolders?$top=250", GraphAPI, folderID)
	}

	for url != "" {
		resp, err := makeRequest("GET", url, token)
		if err != nil {
			return nil, "", err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, "", fmt.Errorf("failed to get folders: status %d", resp.StatusCode)
		}

		var foldersResp FoldersResponse
		if err := json.NewDecoder(resp.Body).Decode(&foldersResp); err != nil {
			return nil, "", err
		}

		for _, folder := range foldersResp.Value {
			currentPath := folder.DisplayName
			if parentPath != "" {
				currentPath = parentPath + "/" + folder.DisplayName
			}

			// Check if this folder matches (case-insensitive)
			if strings.EqualFold(folder.DisplayName, targetName) {
				return &folder, currentPath, nil
			}

			// Also check if target includes path separator and matches the full path
			if strings.Contains(targetName, "/") && strings.EqualFold(currentPath, targetName) {
				return &folder, currentPath, nil
			}

			// Recursively search subfolders
			foundFolder, foundPath, err := findFolderRecursive(token, targetName, folder.ID, currentPath)
			if err != nil {
				return nil, "", err
			}
			if foundFolder != nil {
				return foundFolder, foundPath, nil
			}
		}

		url = foldersResp.NextLink
	}

	return nil, "", nil
}

type deleteResult struct {
	messageID      string
	statusCode     int
	originalStatus int // Track original status before treating 404 as success
	err            error
}

func deleteSingleMessage(messageID, token string) deleteResult {
	retryCount := 0
	delay := RetryDelay

	for retryCount < MaxRetries {
		tokenMutex.Lock()
		token := currentToken
		tokenMutex.Unlock()
		url := fmt.Sprintf("%s/me/messages/%s", GraphAPI, messageID)
		req, err := http.NewRequest("DELETE", url, nil)
		if err != nil {
			return deleteResult{messageID: messageID, err: err}
		}

		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return deleteResult{messageID: messageID, err: err}
		}
		resp.Body.Close()

		// Handle token expiration
		if resp.StatusCode == http.StatusUnauthorized {
			fmt.Printf("    [Msg %s] Token expired, refreshing...\n", messageID)
			newToken, err := refreshAccessToken()
			if err != nil {
				return deleteResult{messageID: messageID, err: fmt.Errorf("refresh failed: %w", err)}
			}
			tokenMutex.Lock()
			currentToken = newToken
			tokenMutex.Unlock()
			continue // Retry with new token
		}

		// Handle rate limiting
		if resp.StatusCode == http.StatusTooManyRequests {
			retryCount++
			if retryCount < MaxRetries {
				fmt.Printf("    [Msg %s] Rate limited, retrying in %v...\n", messageID, delay)
				time.Sleep(delay)
				delay *= 2 // Exponential backoff
				continue
			}
			return deleteResult{messageID: messageID, statusCode: resp.StatusCode}
		}

		// Treat 404 as success (message already deleted/moved)
		if resp.StatusCode == http.StatusNotFound {
			return deleteResult{messageID: messageID, statusCode: http.StatusNoContent, originalStatus: http.StatusNotFound}
		}

		return deleteResult{messageID: messageID, statusCode: resp.StatusCode, originalStatus: resp.StatusCode}
	}

	return deleteResult{messageID: messageID, statusCode: -1, err: fmt.Errorf("max retries exceeded")}
}

func listFolders() error {
	// Get access token
	token, err := getAccessToken()
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	// Display all mail folders recursively
	fmt.Println("Available Folders:")
	fmt.Println()
	if err := displayFoldersRecursive(token, "", ""); err != nil {
		return fmt.Errorf("failed to fetch folders: %w", err)
	}

	return nil
}

func deleteEmails() error {
	// Get token using MSAL
	token, err := getAccessToken()
	if err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}
	fmt.Println("Successfully authenticated!")
	fmt.Println()

	// Find the target folder
	fmt.Printf("Searching for folder: %s...\n", targetFolder)
	targetFolderData, folderPath, err := findFolderRecursive(token, targetFolder, "", "")
	if err != nil {
		return fmt.Errorf("error searching for folder: %w", err)
	}
	if targetFolderData == nil {
		fmt.Printf("Folder '%s' not found!\n", targetFolder)
		fmt.Println("Please check the folder structure above and update target folder")
		return nil
	}

	fmt.Printf("Found folder: %s\n", folderPath)

	// Get folder info to see total count
	fmt.Printf("[API] GET folder info for %s...\n", targetFolderData.ID)
	url := fmt.Sprintf("%s/me/mailFolders/%s", GraphAPI, targetFolderData.ID)
	resp, err := makeRequest("GET", url, token)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to get folder info: status %d", resp.StatusCode)
	}

	var folderData Folder
	if err := json.NewDecoder(resp.Body).Decode(&folderData); err != nil {
		return err
	}

	fmt.Printf("[API] Response received: %d\n", resp.StatusCode)
	totalMessages := folderData.TotalItemCount
	fmt.Printf("Total messages to delete: %d\n\n", totalMessages)

	if totalMessages == 0 {
		fmt.Println("No messages to delete!")
		return nil
	}

	// For large deletions (>10k), ensure we have fresh auth with refresh capability
	if totalMessages > 10000 && usedCachedToken {
		cachePath, err := getTokenCachePath()
		if err == nil && cachePath != "" {
			// Check if cache file exists
			if _, err := os.Stat(cachePath); err == nil {
				// Cache exists - delete it and ask user to re-run
				os.Remove(cachePath)
				fmt.Println("⚠️  LARGE DELETION DETECTED")
				fmt.Printf("This folder contains %d messages, which will take several hours.\n", totalMessages)
				fmt.Println("To ensure uninterrupted operation, please re-run the same command.")
				fmt.Println("You will be prompted to authenticate fresh, enabling automatic token refresh.")
				fmt.Println()
				return nil
			}
		}
	}

	// Delete messages in batches with parallel deletion
	messagesDeleted := 0
	batchSize := 50
	batchNum := 0

	fmt.Println("Deleting messages...")
	startTime := time.Now()

	for messagesDeleted < totalMessages {
		batchNum++
		fmt.Printf("\n[Batch %d] Fetching up to %d messages...\n", batchNum, batchSize)

		// Get messages
		fetchStart := time.Now()
		fmt.Printf("  [API] GET messages batch %d...\n", batchNum)

		// Only request the ID field to minimize response size
		messagesURL := fmt.Sprintf("%s/me/mailFolders/%s/messages?$select=id&$top=%d", GraphAPI, targetFolderData.ID, batchSize)

		// Retry logic for fetching messages
		var resp *http.Response
		var fetchErr error
		for retry := 0; retry < 3; retry++ {
			if retry > 0 {
				fmt.Printf("  Retrying fetch (attempt %d/3)...\n", retry+1)
				time.Sleep(time.Duration(retry) * time.Second)
			}
			resp, fetchErr = makeRequest("GET", messagesURL, token)
			if fetchErr == nil {
				break
			}
		}

		if fetchErr != nil {
			fmt.Printf("  Error in batch %d: %v\n", batchNum, fetchErr)
			break
		}

		fetchTime := time.Since(fetchStart)
		fmt.Printf("  [API] Response received in %.2fs, Status: %d\n", fetchTime.Seconds(), resp.StatusCode)

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			fmt.Printf("  Error in batch %d: status %d, body: %s\n", batchNum, resp.StatusCode, string(body))
			break
		}

		// Read the full response body first
		bodyBytes, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			fmt.Printf("  Error reading response body: %v\n", err)
			break
		}

		var messagesResp MessagesResponse
		if err := json.Unmarshal(bodyBytes, &messagesResp); err != nil {
			fmt.Printf("  Error decoding messages: %v (body length: %d bytes)\n", err, len(bodyBytes))
			// Print first 500 chars of response for debugging
			if len(bodyBytes) > 0 {
				preview := string(bodyBytes)
				if len(preview) > 500 {
					preview = preview[:500] + "..."
				}
				fmt.Printf("  Response preview: %s\n", preview)
			}
			// For truncated responses, wait and retry
			fmt.Println("  Possible server issue, waiting 5 seconds before next attempt...")
			time.Sleep(5 * time.Second)
			continue
		}

		messages := messagesResp.Value
		fmt.Printf("  Got %d messages in this batch\n", len(messages))

		if len(messages) == 0 {
			fmt.Println("  No more messages found, stopping...")
			break
		}

		// Delete messages in parallel
		deleteStart := time.Now()
		batchDeleted := 0

		// Create worker pool
		jobs := make(chan Message, len(messages))
		results := make(chan deleteResult, len(messages))

		var wg sync.WaitGroup
		for w := 0; w < maxWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for msg := range jobs {
					result := deleteSingleMessage(msg.ID, token)
					results <- result
				}
			}()
		}

		// Send jobs
		for _, msg := range messages {
			jobs <- msg
		}
		close(jobs)

		// Wait for all workers to finish
		go func() {
			wg.Wait()
			close(results)
		}()

		// Process results
		msgIdx := 0
		for result := range results {
			if result.err == nil && result.statusCode == http.StatusNoContent {
				messagesDeleted++
				batchDeleted++
				if result.originalStatus == http.StatusNotFound {
					fmt.Printf("    [Msg %d] Already gone (404) (Total: %d/%d)\n", msgIdx, messagesDeleted, totalMessages)
				} else {
					fmt.Printf("    [Msg %d] Deleted (Total: %d/%d)\n", msgIdx, messagesDeleted, totalMessages)
				}
			} else if result.err != nil {
				fmt.Printf("    [Msg %d] Warning: Delete error: %v\n", msgIdx, result.err)
			} else {
				fmt.Printf("    [Msg %d] Warning: Delete returned %d\n", msgIdx, result.statusCode)
			}
			msgIdx++
		}

		deleteTime := time.Since(deleteStart)
		fmt.Printf("  Batch %d completed: %d deleted in %.2fs\n", batchNum, batchDeleted, deleteTime.Seconds())

		// Add delay between batches to avoid rate limiting
		if messagesDeleted < totalMessages {
			time.Sleep(2 * time.Second)
		}
	}

	elapsed := time.Since(startTime)
	fmt.Printf("\n✓ Successfully deleted %d emails from %s in %.1fs\n", messagesDeleted, targetFolder, elapsed.Seconds())

	return nil
}

func parseFlags() error {
	if len(os.Args) < 2 {
		printUsage()
		return fmt.Errorf("missing subcommand")
	}

	subcommand := os.Args[1]

	switch subcommand {
	case "list":
		// list subcommand takes no flags
		return nil

	case "delete":
		// Create a FlagSet for the delete subcommand
		deleteCmd := flag.NewFlagSet("delete", flag.ContinueOnError)
		deleteCmd.StringVar(&targetFolder, "folder", "", "Target folder to delete emails from (required, or use TARGET_FOLDER env var)")
		deleteCmd.IntVar(&maxWorkers, "workers", 5, "Number of parallel deletion workers (or use MAX_WORKERS env var)")

		if err := deleteCmd.Parse(os.Args[2:]); err != nil {
			return err
		}

		// Fall back to environment variables if flags not provided
		if targetFolder == "" {
			targetFolder = os.Getenv("TARGET_FOLDER")
		}
		if maxWorkers == 5 { // Only check env var if flag is default
			if workersEnv := os.Getenv("MAX_WORKERS"); workersEnv != "" {
				if w, err := strconv.Atoi(workersEnv); err == nil {
					maxWorkers = w
				}
			}
		}

		// Validate required parameters for delete mode
		if targetFolder == "" {
			printUsage()
			return fmt.Errorf("folder parameter required for delete subcommand")
		}

		// Validate workers range
		if maxWorkers < 1 || maxWorkers > 10 {
			return fmt.Errorf("workers must be between 1 and 10")
		}

		return nil

	default:
		printUsage()
		return fmt.Errorf("unknown subcommand: %s", subcommand)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, "Outlook Email Deleter\n\n")
	fmt.Fprintf(os.Stderr, "Usage: %s <subcommand> [options]\n\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "Subcommands:\n")
	fmt.Fprintf(os.Stderr, "  list                    Display all folders with message counts\n")
	fmt.Fprintf(os.Stderr, "  delete [options]        Delete emails from a specific folder\n")
	fmt.Fprintf(os.Stderr, "\nDelete options:\n")
	fmt.Fprintf(os.Stderr, "  -folder <path>          Target folder path (required or set TARGET_FOLDER)\n")
	fmt.Fprintf(os.Stderr, "  -workers <n>            Number of parallel workers (default: 5)\n")
	fmt.Fprintf(os.Stderr, "\nEnvironment Variables:\n")
	fmt.Fprintf(os.Stderr, "  TARGET_FOLDER           Target folder path (alternative to -folder flag)\n")
	fmt.Fprintf(os.Stderr, "  MAX_WORKERS             Number of parallel workers (alternative to -workers flag)\n")
	fmt.Fprintf(os.Stderr, "\nExamples:\n")
	fmt.Fprintf(os.Stderr, "  %s list\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s delete -folder \"Deleted Items\"\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s delete -folder \"SOC/Support\" -workers 2\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  TARGET_FOLDER=\"Deleted Items\" %s delete\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "\n")
}

func main() {
	if err := parseFlags(); err != nil {
		os.Exit(1)
	}

	subcommand := os.Args[1]

	if subcommand == "list" {
		fmt.Println("Mode: List Folders")
		fmt.Println()
		if err := listFolders(); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	} else if subcommand == "delete" {
		fmt.Println("Mode: Delete Emails")
		fmt.Printf("Target Folder: %s\n", targetFolder)
		fmt.Printf("Max Workers: %d\n\n", maxWorkers)

		if err := deleteEmails(); err != nil {
			fmt.Printf("Error: %v\n", err)
			os.Exit(1)
		}
	}
}
