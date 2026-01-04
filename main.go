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
// Default credentials for hardcoded org; override with env vars for other orgs
const (
	DefaultClientID = "51b31bec-d0d8-4939-8b58-e53f56b9e373"
	DefaultTenantID = "79ba327a-98fe-4f8e-83ac-e9596bac64dc"

	GraphAPI                 = "https://graph.microsoft.com/v1.0"
	MaxRetries               = 3
	RetryDelay               = 1 * time.Second
	TokenBufferDuration      = 5 * time.Minute
	ProactiveRefreshInterval = 10 * time.Minute
	ProactiveRefreshBuffer   = 15 * time.Minute
	BatchDelayDuration       = 2 * time.Second
	RetryWaitDuration        = 5 * time.Second
	MaxFolderDepth           = 50
	AuthContextTimeout       = 5 * time.Minute
	HTTPTimeout              = 60 * time.Second
	HTTPResponseTimeout      = 30 * time.Second
	DeletionHTTPTimeout      = 30 * time.Second
)

// Runtime configuration (from env vars or defaults)
var (
	ClientID        string
	TenantID        string
	targetFolder    string
	maxWorkers      int
	permanentDelete bool
)

func init() {
	// Support env var overrides for other organizations
	ClientID = getEnvOrDefault("OUTLOOK_CLIENT_ID", DefaultClientID)
	TenantID = getEnvOrDefault("OUTLOOK_TENANT_ID", DefaultTenantID)
}

func getEnvOrDefault(envKey, defaultValue string) string {
	if val := os.Getenv(envKey); val != "" {
		return val
	}
	return defaultValue
}

// Global token state for automatic refresh
var (
	currentToken     string
	tokenExpiry      time.Time
	tokenMutex       sync.Mutex
	msalClient       public.Client
	lastAuthAccount  public.Account
	usedCachedToken  bool                          // Track if we used cache at startup
	isRefreshing     bool                          // Flag to prevent thundering herd
	refreshCondition = sync.NewCond(&sync.Mutex{}) // Condition variable for refresh coordination
	msalClientOnce   sync.Once
)

// Global HTTP clients
var (
	httpClient         *http.Client
	deletionHTTPClient *http.Client
	httpClientOnce     sync.Once
	deletionClientOnce sync.Once
)

var (
	scopes = []string{"https://graph.microsoft.com/Mail.ReadWrite"}
)

func initHTTPClients() {
	httpClientOnce.Do(func() {
		httpClient = &http.Client{
			Timeout: HTTPTimeout,
			Transport: &http.Transport{
				ResponseHeaderTimeout: HTTPResponseTimeout,
				DisableKeepAlives:     false,
			},
		}
	})
	deletionClientOnce.Do(func() {
		deletionHTTPClient = &http.Client{Timeout: DeletionHTTPTimeout}
	})
}

func getMSALClient() (public.Client, error) {
	var err error
	msalClientOnce.Do(func() {
		authority := fmt.Sprintf("https://login.microsoftonline.com/%s", TenantID)
		msalClient, err = public.New(ClientID, public.WithAuthority(authority))
	})
	return msalClient, err
}

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
	// Use OS-specific cache dir (LOCALAPPDATA on Windows, ~/.cache otherwise)
	cacheRoot, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	cacheDir := filepath.Join(cacheRoot, "outlook-deleter")
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "token.json"), nil
}

func cacheToken(accessToken, accountID string, expiresIn int64) error {
	cachePath, err := getTokenCachePath()
	if err != nil {
		fmt.Printf("Warning: Unable to get cache path: %v\n", err)
		return err
	}

	cache := TokenCache{
		AccessToken: accessToken,
		ExpiresAt:   time.Now().Add(time.Duration(expiresIn) * time.Second),
		AccountID:   accountID,
	}

	data, err := json.Marshal(cache)
	if err != nil {
		fmt.Printf("Warning: Failed to marshal token cache: %v\n", err)
		return err
	}

	if err := os.WriteFile(cachePath, data, 0600); err != nil {
		fmt.Printf("Warning: Failed to write token cache: %v\n", err)
		return err
	}
	return nil
}

func refreshAccessToken() (string, error) {
	// Use condition variable to coordinate multiple refresh attempts
	refreshCondition.L.Lock()
	for isRefreshing {
		// Another goroutine is already refreshing, wait for it to complete
		refreshCondition.Wait()
	}

	// Check if token is still valid after waiting (read with lock)
	if time.Now().Before(tokenExpiry.Add(-TokenBufferDuration)) {
		refreshCondition.L.Unlock()
		return currentToken, nil
	}

	isRefreshing = true
	refreshCondition.L.Unlock()
	defer func() {
		refreshCondition.L.Lock()
		isRefreshing = false
		refreshCondition.Broadcast() // Notify waiting goroutines
		refreshCondition.L.Unlock()
	}()

	tokenMutex.Lock()
	defer tokenMutex.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), AuthContextTimeout)
	defer cancel()

	// Try silent refresh with the last authenticated account
	if lastAuthAccount.HomeAccountID != "" {
		client, err := getMSALClient()
		if err != nil {
			fmt.Printf("Warning: Failed to get MSAL client: %v\n", err)
			return "", fmt.Errorf("unable to get MSAL client: %w", err)
		}

		result, err := client.AcquireTokenSilent(ctx, scopes, public.WithSilentAccount(lastAuthAccount))
		if err == nil {
			// Check if token is actually valid (not already expired)
			if time.Now().Before(result.ExpiresOn.Add(-TokenBufferDuration)) {
				currentToken = result.AccessToken
				tokenExpiry = result.ExpiresOn
				expiresIn := result.ExpiresOn.Unix() - time.Now().Unix()
				_ = cacheToken(result.AccessToken, result.Account.HomeAccountID, expiresIn)
				fmt.Println("\n✓ Token automatically refreshed")
				return result.AccessToken, nil
			}
			fmt.Printf("\nWarning: Refreshed token is already expired (expires at %v, now %v)\n", result.ExpiresOn, time.Now())
		}
		fmt.Printf("\nWarning: Token refresh failed (%v)\n", err)
	}

	// If silent refresh fails, need to re-authenticate
	fmt.Println("\nToken refresh failed - re-authentication required")
	return "", fmt.Errorf("unable to refresh token - silent refresh failed or token already expired")
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
	client, err := getMSALClient()
	if err != nil {
		return "", fmt.Errorf("failed to create public client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), AuthContextTimeout)
	defer cancel()

	// Try to get from local cache first
	cachePath, err := getTokenCachePath()
	if err == nil && cachePath != "" {
		data, err := os.ReadFile(cachePath)
		if err == nil {
			var cache TokenCache
			if err := json.Unmarshal(data, &cache); err == nil {
				// Check if access token is still valid (with buffer)
				if time.Now().Add(TokenBufferDuration).Before(cache.ExpiresAt) {
					fmt.Println("✓ Using cached access token")
					usedCachedToken = true

					// Try to load the account for future refreshes
					accounts, err := client.Accounts(ctx)
					if err != nil {
						fmt.Printf("Warning: Could not load cached account: %v\n", err)
					} else {
						for _, acc := range accounts {
							if acc.HomeAccountID == cache.AccountID {
								lastAuthAccount = acc
								break
							}
						}
					}

					currentToken = cache.AccessToken
					tokenExpiry = cache.ExpiresAt
					return cache.AccessToken, nil
				}
			}
		}
	}

	// Try to get token from MSAL cache
	accounts, err := client.Accounts(ctx)
	if err != nil {
		fmt.Printf("Cache check: no accounts found (%v)\n", err)
	} else if len(accounts) > 0 {
		fmt.Printf("Cache check: found account %s\n", accounts[0].PreferredUsername)
		result, err := client.AcquireTokenSilent(ctx, scopes, public.WithSilentAccount(accounts[0]))
		if err == nil {
			lastAuthAccount = accounts[0]
			usedCachedToken = true
			tokenExpiry = result.ExpiresOn
			_ = cacheToken(result.AccessToken, result.Account.HomeAccountID, result.ExpiresOn.Unix()-time.Now().Unix())
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

		deviceCode, err := client.AcquireTokenByDeviceCode(ctx, scopes)
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
		result, err = client.AcquireTokenInteractive(ctx, scopes)
		if err != nil {
			return "", fmt.Errorf("interactive authentication failed: %w", err)
		}
	}

	// Store the authenticated account for future refreshes
	lastAuthAccount = result.Account
	tokenExpiry = result.ExpiresOn
	expiresIn := result.ExpiresOn.Unix() - time.Now().Unix()
	_ = cacheToken(result.AccessToken, result.Account.HomeAccountID, expiresIn)
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

	initHTTPClients()
	return httpClient.Do(req)
}

func displayFoldersRecursive(token, folderID, indent string, depth int) error {
	if depth > MaxFolderDepth {
		fmt.Printf("%s[Max folder depth reached]\n", indent)
		return nil
	}

	url := fmt.Sprintf("%s/me/mailFolders?$top=250", GraphAPI)
	if folderID != "" {
		url = fmt.Sprintf("%s/me/mailFolders/%s/childFolders?$top=250", GraphAPI, folderID)
	}

	for url != "" {
		resp, err := makeRequest("GET", url, token)
		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			newToken, refreshErr := refreshAccessToken()
			if refreshErr != nil {
				return fmt.Errorf("token expired and refresh failed: %w", refreshErr)
			}
			token = newToken
			resp, err = makeRequest("GET", url, token)
			if err != nil {
				return err
			}
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
			if err := displayFoldersRecursive(token, folder.ID, indent+"  ", depth+1); err != nil {
				return err
			}
		}

		url = foldersResp.NextLink
	}

	return nil
}

func findFolderRecursive(token, targetName, folderID, parentPath string, depth int) (*Folder, string, error) {
	if depth > MaxFolderDepth {
		return nil, "", fmt.Errorf("max folder depth exceeded while searching for '%s'", targetName)
	}

	url := fmt.Sprintf("%s/me/mailFolders?$top=250", GraphAPI)
	if folderID != "" {
		url = fmt.Sprintf("%s/me/mailFolders/%s/childFolders?$top=250", GraphAPI, folderID)
	}

	for url != "" {
		resp, err := makeRequest("GET", url, token)
		if err != nil {
			return nil, "", err
		}

		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close()
			newToken, refreshErr := refreshAccessToken()
			if refreshErr != nil {
				return nil, "", fmt.Errorf("token expired and refresh failed: %w", refreshErr)
			}
			token = newToken
			resp, err = makeRequest("GET", url, token)
			if err != nil {
				return nil, "", err
			}
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
			foundFolder, foundPath, err := findFolderRecursive(token, targetName, folder.ID, currentPath, depth+1)
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

	for {
		tokenMutex.Lock()
		token := currentToken
		tokenMutex.Unlock()

		var url string
		var method string
		if permanentDelete {
			url = fmt.Sprintf("%s/me/messages/%s/permanentDelete", GraphAPI, messageID)
			method = "POST"
		} else {
			url = fmt.Sprintf("%s/me/messages/%s", GraphAPI, messageID)
			method = "DELETE"
		}

		req, err := http.NewRequest(method, url, nil)
		if err != nil {
			return deleteResult{messageID: messageID, err: err}
		}

		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		if permanentDelete {
			req.Header.Set("Content-Length", "0")
		}

		initHTTPClients()
		resp, err := deletionHTTPClient.Do(req)
		if err != nil {
			return deleteResult{messageID: messageID, err: err}
		}
		resp.Body.Close()

		// Handle token expiration (only retry once per request)
		if resp.StatusCode == http.StatusUnauthorized {
			if retryCount < MaxRetries {
				fmt.Printf("    [Msg %s] Token expired, refreshing...\n", messageID)
				newToken, err := refreshAccessToken()
				if err != nil {
					return deleteResult{messageID: messageID, err: fmt.Errorf("refresh failed: %w", err)}
				}
				tokenMutex.Lock()
				currentToken = newToken
				tokenMutex.Unlock()
				retryCount++
				continue // Retry with new token
			}
			return deleteResult{messageID: messageID, statusCode: resp.StatusCode}
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
}

// startProactiveTokenRefresh starts a background goroutine that refreshes the token every 10 minutes
func startProactiveTokenRefresh(stopChan <-chan bool) {
	go func() {
		ticker := time.NewTicker(ProactiveRefreshInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				// Check if token needs refresh (if expiring within buffer) - read with lock
				tokenMutex.Lock()
				needsRefresh := time.Now().After(tokenExpiry.Add(-ProactiveRefreshBuffer))
				tokenMutex.Unlock()

				if !needsRefresh {
					continue // Token is still good
				}
				fmt.Println("[Background] Proactively refreshing token...")
				_, err := refreshAccessToken()
				if err != nil {
					fmt.Printf("[Background] Token refresh failed: %v\n", err)
				}
			case <-stopChan:
				return
			}
		}
	}()
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
	if err := displayFoldersRecursive(token, "", "", 0); err != nil {
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

	// Start proactive token refresh in background
	stopChan := make(chan bool)
	startProactiveTokenRefresh(stopChan)
	defer func() {
		stopChan <- true
	}()

	// Find the target folder
	fmt.Printf("Searching for folder: %s...\n", targetFolder)
	targetFolderData, folderPath, err := findFolderRecursive(token, targetFolder, "", "", 0)
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

		if resp.StatusCode == http.StatusUnauthorized {
			fmt.Println("  Token expired, attempting refresh...")
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			fmt.Printf("  Error in batch %d: status %d, body: %s\n", batchNum, resp.StatusCode, string(body))

			// Try to refresh the token
			newToken, refreshErr := refreshAccessToken()
			if refreshErr != nil {
				fmt.Printf("  Token refresh failed: %v\n", refreshErr)
				fmt.Println("  Unable to continue - token cannot be refreshed")
				return fmt.Errorf("batch %d failed with 401 and token refresh failed: %w", batchNum, refreshErr)
			}

			// Update token and retry the entire batch
			tokenMutex.Lock()
			token = newToken
			tokenMutex.Unlock()
			fmt.Println("  Token refreshed, retrying batch...")
			continue
		}

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
			fmt.Println("  Possible server issue, waiting before next attempt...")
			time.Sleep(RetryWaitDuration)
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
			time.Sleep(BatchDelayDuration)
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
		deleteCmd.BoolVar(&permanentDelete, "permanent", false, "Permanently delete messages (bypass Deleted Items folder)")

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
		if !permanentDelete {
			permEnv := os.Getenv("PERMANENT_DELETE")
			if permEnv == "true" || permEnv == "1" {
				permanentDelete = true
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
	fmt.Fprintf(os.Stderr, "  -permanent              Permanently delete messages (bypass Deleted Items)\n")
	fmt.Fprintf(os.Stderr, "\nEnvironment Variables:\n")
	fmt.Fprintf(os.Stderr, "  TARGET_FOLDER           Target folder path (alternative to -folder flag)\n")
	fmt.Fprintf(os.Stderr, "  MAX_WORKERS             Number of parallel workers (alternative to -workers flag)\n")
	fmt.Fprintf(os.Stderr, "  PERMANENT_DELETE        Set to 'true' or '1' for permanent deletion\n")
	fmt.Fprintf(os.Stderr, "  OUTLOOK_CLIENT_ID       Azure app client ID (default: built-in org)\n")
	fmt.Fprintf(os.Stderr, "  OUTLOOK_TENANT_ID       Azure tenant ID (default: built-in org)\n")
	fmt.Fprintf(os.Stderr, "\nExamples:\n")
	fmt.Fprintf(os.Stderr, "  %s list\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s delete -folder \"Deleted Items\"\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s delete -folder \"SOC/Support\" -workers 2\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  %s delete -folder \"Junk Email\" -permanent\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  TARGET_FOLDER=\"Deleted Items\" %s delete\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "  OUTLOOK_CLIENT_ID=xxx OUTLOOK_TENANT_ID=yyy %s list\n", os.Args[0])
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
