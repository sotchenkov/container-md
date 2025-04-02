package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	defaultCheckInterval      = 60 * time.Second // Check every 60 seconds
	defaultRestartWaitTimeout = 90 * time.Second // Wait 90 seconds after restart before checking health
	defaultLogTail            = "10"             // Number of log lines to fetch
)

type Config struct {
	CheckInterval      time.Duration
	RestartWaitTimeout time.Duration
	LogTail            string
	TelegramBotToken   string
	TelegramChatID     int64
	DockerHost         string // Optional: e.g., "unix:///var/run/docker.sock" or "tcp://127.0.0.1:2375"
}

// Keep track of containers we recently tried to restart to avoid loops
var (
	restartingMutex sync.Mutex
	restartingNow   = make(map[string]bool) // Key: Container ID
)

func main() {
	cfg := loadConfig()

	log.Println("Initializing Docker client...")
	cli, err := createDockerClient(cfg.DockerHost)
	if err != nil {
		log.Fatalf("❌ Failed to create Docker client: %v", err)
	}
	defer cli.Close()

	// Test Docker connection
	_, err = cli.Ping(context.Background())
	if err != nil {
		log.Fatalf("❌ Failed to ping Docker daemon: %v", err)
	}
	log.Println("✅ Docker client connected successfully.")

	log.Println("Initializing Telegram bot...")
	bot, err := tgbotapi.NewBotAPI(cfg.TelegramBotToken)
	if err != nil {
		log.Fatalf("❌ Failed to initialize Telegram bot: %v", err)
	}
	bot.Debug = false // Set to true for more verbose Telegram output
	log.Printf("✅ Authorized on account %s", bot.Self.UserName)

	log.Printf("🚀 Starting Docker health monitor. Check interval: %v, Restart wait timeout: %v", cfg.CheckInterval, cfg.RestartWaitTimeout)

	// Run initial check immediately
	checkContainers(cli, bot, &cfg)

	// Start ticker for periodic checks
	ticker := time.NewTicker(cfg.CheckInterval)
	defer ticker.Stop()

	for range ticker.C {
		checkContainers(cli, bot, &cfg)
	}
}

// loadConfig loads configuration from environment variables
func loadConfig() Config {
	cfg := Config{
		LogTail: defaultLogTail,
	}

	// Telegram
	cfg.TelegramBotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	if cfg.TelegramBotToken == "" {
		log.Fatal("❌ TELEGRAM_BOT_TOKEN environment variable not set.")
	}
	chatIDStr := os.Getenv("TELEGRAM_CHAT_ID")
	if chatIDStr == "" {
		log.Fatal("❌ TELEGRAM_CHAT_ID environment variable not set.")
	}
	cfg.TelegramChatID, _ = strconv.ParseInt(chatIDStr, 10, 64)
	if cfg.TelegramChatID == 0 {
		log.Fatal("❌ Invalid TELEGRAM_CHAT_ID environment variable.")
	}

	// Docker
	cfg.DockerHost = os.Getenv("DOCKER_HOST") // If empty, docker client uses default

	// Timings
	checkIntervalStr := os.Getenv("CHECK_INTERVAL_SECONDS")
	if interval, err := strconv.Atoi(checkIntervalStr); err == nil && interval > 0 {
		cfg.CheckInterval = time.Duration(interval) * time.Second
	} else {
		cfg.CheckInterval = defaultCheckInterval
	}

	restartTimeoutStr := os.Getenv("RESTART_WAIT_TIMEOUT_SECONDS")
	if timeout, err := strconv.Atoi(restartTimeoutStr); err == nil && timeout > 0 {
		cfg.RestartWaitTimeout = time.Duration(timeout) * time.Second
	} else {
		cfg.RestartWaitTimeout = defaultRestartWaitTimeout
	}

	logTailStr := os.Getenv("LOG_TAIL_LINES")
	if tail, err := strconv.Atoi(logTailStr); err == nil && tail > 0 {
		cfg.LogTail = strconv.Itoa(tail)
	} else {
		cfg.LogTail = defaultLogTail // Use default if invalid or not set
	}

	return cfg
}

// createDockerClient initializes the Docker client
func createDockerClient(dockerHost string) (*client.Client, error) {
	if dockerHost != "" {
		log.Printf("Attempting to connect to Docker host: %s", dockerHost)
		return client.NewClientWithOpts(client.FromEnv, client.WithHost(dockerHost), client.WithAPIVersionNegotiation())
	}
	log.Println("Connecting to default Docker host...")
	return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
}

// checkContainers lists containers and checks their health
func checkContainers(cli *client.Client, bot *tgbotapi.BotAPI, cfg *Config) {
	log.Println("🔍 Checking container health...")
	containers, err := cli.ContainerList(context.Background(), container.ListOptions{All: false}) // СТАЛО (используем container.ListOptions)
	if err != nil {
		log.Printf("⚠️ Error listing containers: %v", err)
		return
	}

	for _, cont := range containers {
		inspectData, err := cli.ContainerInspect(context.Background(), cont.ID)
		if err != nil {
			log.Printf("⚠️ Error inspecting container %s (%s): %v", getContainerName(cont), cont.ID[:12], err)
			continue
		}

		// Check if health check is configured and if status is unhealthy
		if inspectData.State != nil && inspectData.State.Health != nil {
			healthStatus := inspectData.State.Health.Status
			contName := getContainerName(cont)
			contID := cont.ID

			// Debug log for health status
			// log.Printf("Container: %s, Health Status: %s", contName, healthStatus)
			if healthStatus == types.Unhealthy {
				restartingMutex.Lock()
				if restartingNow[contID] {
					log.Printf("⏳ Container %s (%s) is already being processed for restart.", contName, contID[:12])
					restartingMutex.Unlock()
					continue // Skip if already handling this specific container instance
				}
				// Mark as being processed
				restartingNow[contID] = true
				restartingMutex.Unlock()

				log.Printf("🚨 Unhealthy container detected: %s (%s)", contName, contID[:12])
				// Handle unhealthy container in a separate goroutine
				// Pass necessary data that won't change
				go handleUnhealthyContainer(cli, bot, cfg, contID, contName)

			}
		} else if inspectData.State != nil && inspectData.State.Health == nil {
			// Optional: Log containers without health checks configured
			// log.Printf("ℹ️ Container %s (%s) has no health check configured.", getContainerName(cont), cont.ID[:12])
		}
	}
	log.Println("✅ Container health check finished.")
}

// handleUnhealthyContainer fetches logs, notifies, restarts, and checks again
func handleUnhealthyContainer(cli *client.Client, bot *tgbotapi.BotAPI, cfg *Config, containerID, containerName string) {
	// Ensure we remove the container from the processing map when done
	defer func() {
		restartingMutex.Lock()
		delete(restartingNow, containerID)
		restartingMutex.Unlock()
		log.Printf("Finished processing for container %s (%s)", containerName, containerID[:12])
	}()

	// 1. Get Logs
	logs, err := getContainerLogs(cli, containerID, cfg.LogTail)
	if err != nil {
		log.Printf("⚠️ Failed to get logs for %s (%s): %v", containerName, containerID[:12], err)
		logs = "Failed to retrieve logs." // Provide placeholder text
	}

	// 2. Send Initial Notification
	message := fmt.Sprintf("🚨 *%s* is unhealthy!\n\n*Last logs (%s lines):*\n```\n%s\n```\n\n*Action:* Trying to restart...",
		escapeMarkdown(containerName),
		cfg.LogTail,
		escapeMarkdown(logs),
	)
	sendTelegramNotification(bot, cfg.TelegramChatID, message)

	// 3. Restart Container
	log.Printf("🔄 Attempting to restart container %s (%s)...", containerName, containerID[:12])
	restartCmdTimeoutSeconds := 10
	stopOptions := container.StopOptions{Timeout: &restartCmdTimeoutSeconds}
	err = cli.ContainerRestart(context.Background(), containerID, stopOptions)
	if err != nil {
		log.Printf("❌ Failed to initiate restart for %s (%s): %v", containerName, containerID[:12], err)
		errorMessage := fmt.Sprintf("❌ Failed to initiate restart for *%s*.\nError: `%s`",
			escapeMarkdown(containerName),
			escapeMarkdown(err.Error()),
		)
		sendTelegramNotification(bot, cfg.TelegramChatID, errorMessage)
		return // Don't proceed if restart command failed
	}
	log.Printf("✅ Restart command sent for %s (%s). Waiting %v for stabilization...", containerName, containerID[:12], cfg.RestartWaitTimeout)

	// 4. Wait for container to potentially stabilize
	time.Sleep(cfg.RestartWaitTimeout)

	// 5. Check Health Status Again
	log.Printf("🔎 Checking health status of %s (%s) after restart...", containerName, containerID[:12])
	inspectData, err := cli.ContainerInspect(context.Background(), containerID)
	finalMessage := ""

	if err != nil {
		// Handle case where container might not exist anymore after restart attempt
		if client.IsErrNotFound(err) {
			log.Printf("⚠️ Container %s (%s) not found after restart attempt.", containerName, containerID[:12])
			finalMessage = fmt.Sprintf("⚠️ Container *%s* not found after restart attempt. It might have been removed or failed to start.",
				escapeMarkdown(containerName),
			)
		} else {
			log.Printf("⚠️ Error inspecting container %s (%s) after restart: %v", containerName, containerID[:12], err)
			finalMessage = fmt.Sprintf("⚠️ Error checking status for *%s* after restart.\nError: `%s`",
				escapeMarkdown(containerName),
				escapeMarkdown(err.Error()),
			)
		}
	} else {
		// Container found, check its health
		if inspectData.State != nil && inspectData.State.Health != nil {
			finalStatus := inspectData.State.Health.Status
			log.Printf("ℹ️ Final health status for %s (%s): %s", containerName, containerID[:12], finalStatus)
			if finalStatus == types.Healthy {
				finalMessage = fmt.Sprintf("✅ Successfully restarted *%s*. It is now healthy.",
					escapeMarkdown(containerName),
				)
			} else {
				finalMessage = fmt.Sprintf("❌ Failed to restore *%s*. It is still *%s* after restart.",
					escapeMarkdown(containerName),
					escapeMarkdown(finalStatus),
				)
			}
		} else if inspectData.State != nil && inspectData.State.Running {
			// Running but no health check or health check status not available yet
			log.Printf("❓ Container %s (%s) is running after restart, but health status is unavailable/not configured.", containerName, containerID[:12])
			finalMessage = fmt.Sprintf("❓ Container *%s* is running after restart, but health status is unavailable or not configured. Manual check recommended.",
				escapeMarkdown(containerName),
			)
		} else {
			// Not running
			log.Printf("❌ Container %s (%s) is not running after restart attempt. Final state: %s", containerName, containerID[:12], inspectData.State.Status)
			finalMessage = fmt.Sprintf("❌ Container *%s* failed to stay running after restart. Final state: *%s*",
				escapeMarkdown(containerName),
				escapeMarkdown(inspectData.State.Status),
			)
		}
	}

	// 6. Send Final Notification
	sendTelegramNotification(bot, cfg.TelegramChatID, finalMessage)
}

func getContainerName(c container.Summary) string { 
	if len(c.Names) > 0 {
		// Docker container names often start with '/', remove it
		for _, name := range c.Names {
			cleanedName := strings.TrimPrefix(name, "/")
			if cleanedName != "" { 
				return cleanedName
			}
		}
		return c.Names[0]

	}
	// Fallback to image name or ID if no name is available
	if c.Image != "" {
		return fmt.Sprintf("%s (Image: %s)", c.ID[:12], c.Image)
	}
	return c.ID[:12]
}

// getContainerLogs fetches the last N lines of logs for a container
func getContainerLogs(cli *client.Client, containerID, tail string) (string, error) {
	logOptions := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       tail,  // Get only the last N lines
		Timestamps: false, // Add timestamps if needed: Timestamps: true
	}

	logReader, err := cli.ContainerLogs(context.Background(), containerID, logOptions)
	if err != nil {
		return "", fmt.Errorf("failed to get log reader: %w", err)
	}
	defer logReader.Close()

	var logBuf bytes.Buffer
	_, err = io.Copy(&logBuf, logReader)
	if err != nil {
		return "", fmt.Errorf("failed to copy logs: %w", err)
	}

	// Docker might prepend 8 bytes of header info per line in non-TTY mode, try to strip it
	// This simple approach might not be perfect for interleaved streams
	cleanedLog := stripDockerLogHeaders(logBuf.Bytes())

	return cleanedLog, nil
}

// stripDockerLogHeaders attempts to remove the 8-byte Docker log stream header.
// See: https://docs.docker.com/engine/api/v1.41/#tag/Container/operation/ContainerAttach
func stripDockerLogHeaders(logBytes []byte) string {
	var result strings.Builder
	buf := bytes.NewBuffer(logBytes)
	hdr := make([]byte, 8)

	for {
		n, err := buf.Read(hdr)
		if err == io.EOF {
			break // End of stream
		}
		if err != nil || n < 8 {
			// Couldn't read header, maybe it's plain text already or truncated?
			// Write the remaining buffer content
			result.Write(hdr[:n])     // Write what was read
			result.Write(buf.Bytes()) // Write the rest
			break
		}

		// Decode the size from the header (last 4 bytes)
		size := int(hdr[4])<<24 | int(hdr[5])<<16 | int(hdr[6])<<8 | int(hdr[7])

		if size <= 0 || size > buf.Len() {
			// Invalid size or not enough data left, assume rest is text
			result.Write(hdr)         // Write the potential header bytes back
			result.Write(buf.Bytes()) // Write the rest
			break
		}

		// Read the actual log line payload
		payload := make([]byte, size)
		n, err = buf.Read(payload)
		if err != nil || n < size {
			// Couldn't read payload
			result.WriteString("[Error reading log payload]\n")
			break
		}
		result.Write(payload)
	}
	return result.String()

}

// sendTelegramNotification sends a message using the bot
func sendTelegramNotification(bot *tgbotapi.BotAPI, chatID int64, message string) {
	log.Printf("✉️ Sending notification to chat %d", chatID)
	msg := tgbotapi.NewMessage(chatID, message)
	msg.ParseMode = tgbotapi.ModeMarkdown // Use Markdown for formatting
	if _, err := bot.Send(msg); err != nil {
		log.Printf("⚠️ Failed to send Telegram message: %v", err)
	}
}

// escapeMarkdown escapes characters that have special meaning in Telegram Markdown
func escapeMarkdown(text string) string {
	// Characters to escape: _, *, [, ], (, ), ~, `, >, #, +, -, =, |, {, }, ., !
	replacer := strings.NewReplacer(
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	)
	return replacer.Replace(text)
}
