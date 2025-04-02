# container-md - Docker health monitor that automatically restarts and notifies about unhealthy containers

Container-md is a simple yet effective Go utility designed to monitor the health status of your running Docker containers. When a container becomes `unhealthy`, container-md takes action: it notifies you via Telegram with recent logs, attempts to restart the container, and reports back on the success or failure of the recovery attempt.

## Features

*   **Health Monitoring:** Periodically checks the health status of running Docker containers.
*   **Unhealthy Detection:** Identifies containers marked as `unhealthy` by Docker's health check mechanism.
*   **Telegram Notifications:** Sends instant alerts to a specified Telegram chat when an unhealthy container is detected.
*   **Log Snippets:** Includes the last few lines of the container's logs in the initial notification for quick diagnosis.
*   **Automatic Restart:** Attempts to restart the unhealthy container.
*   **Outcome Reporting:** Sends a follow-up Telegram message indicating whether the restart resolved the unhealthy state or not.
*   **Configurable Intervals:** Allows customization of the health check frequency and the timeout for waiting after a restart.
*   **Concurrency Safe:** Prevents multiple restart attempts on the same container instance simultaneously.
*   **Environment Variable Configuration:** Easy setup using environment variables.
*   **Docker Socket/Host Support:** Connects to the Docker daemon via the standard socket or a specified `DOCKER_HOST`.

## Prerequisites

*   **Go:** Version 1.18 or later (only needed if building from source).
*   **Docker:** Docker daemon running and accessible. The user running container-md  needs permission to access the Docker socket or API.
*   **Telegram Bot:**
    *   A Telegram Bot Token obtained from BotFather.
    *   The Chat ID of the Telegram user or group where notifications should be sent.

## Configuration

Container-md is configured using environment variables:

| Variable                     | Required | Default                    | Description                                                                 |
| :--------------------------- | :------- | :------------------------- | :-------------------------------------------------------------------------- |
| `TELEGRAM_BOT_TOKEN`         | **Yes**  |                            | Your Telegram Bot API token.                                                |
| `TELEGRAM_CHAT_ID`           | **Yes**  |                            | The target Telegram Chat ID for notifications.                              |
| `CHECK_INTERVAL_SECONDS`     | No       | `60`                       | Interval (in seconds) between container health checks.                      |
| `RESTART_WAIT_TIMEOUT_SECONDS` | No       | `90`                       | Time (in seconds) to wait after restarting before checking health again.  |
| `LOG_TAIL_LINES`             | No       | `10`                       | Number of recent log lines to include in the notification.                  |
| `DOCKER_HOST`                | No       | Docker Client Default      | Optional. Specify Docker daemon host (e.g., `unix:///var/run/docker.sock`). |

## Installation & Usage

### Option 1: Running the Binary

1.  **Clone the repository (Optional):**
    ```bash
    git clone https://github.com/sotchenkov/container-md.git # Replace with your repo path
    cd container-md 
    ```
2.  **Build:**
    ```bash
    go build -o container-md  .
    ```
3.  **Set Environment Variables:**
    ```bash
    export TELEGRAM_BOT_TOKEN="YOUR_BOT_TOKEN"
    export TELEGRAM_CHAT_ID="YOUR_CHAT_ID"
    # Optional:
    # export CHECK_INTERVAL_SECONDS="30"
    # export RESTART_WAIT_TIMEOUT_SECONDS="60"
    # export LOG_TAIL_LINES="15"
    # export DOCKER_HOST="unix:///var/run/docker.sock"
    ```
4.  **Run:**
    ```bash
    ./container-md 
    ```

### Option 2: Running with Docker

1.  **Build the Docker Image:**
    ```bash
    docker build -t container-md .
    ```
2.  **Run the Docker Container:**
    ```bash
    docker run --rm -d \
      --name container-md  \
      -e TELEGRAM_BOT_TOKEN="YOUR_BOT_TOKEN" \
      -e TELEGRAM_CHAT_ID="YOUR_CHAT_ID" \
      -e CHECK_INTERVAL_SECONDS="60" \
      -e RESTART_WAIT_TIMEOUT_SECONDS="90" \
      -e LOG_TAIL_LINES="10" \
      -v /var/run/docker.sock:/var/run/docker.sock \
      container-md 
    ```
    *Note: Adjust the volume mount (`-v`) if your Docker socket path is different or if using TCP (`DOCKER_HOST` env var).*

## How it Works

1.  **Check Loop:** container-md periodically lists running containers via the Docker API.
2.  **Inspect:** For each container, it inspects its state, specifically looking for the `Health` status.
3.  **Detect Unhealthy:** If a container's health status is `unhealthy`:
    *   It marks the container ID as being processed to avoid duplicate actions.
    *   It fetches the last N log lines from the container.
    *   It sends an initial notification to Telegram with the container name and log snippet.
4.  **Restart:** container-md  issues a restart command to the unhealthy container.
5.  **Wait & Re-Check:** It waits for a configured duration (`RESTART_WAIT_TIMEOUT_SECONDS`) to allow the container time to stabilize after restarting. Then, it inspects the container again.
6.  **Final Notification:** Based on the container's state and health status after the wait:
    *   If `healthy`: Sends a success notification.
    *   If still `unhealthy` or in another non-healthy running state: Sends a failure notification.
    *   If the container is stopped or not found: Sends an appropriate failure notification.
7.  **Unlock:** Removes the container ID from the "being processed" list.
8.  **Repeat:** The check loop continues.

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request or open an Issue.

## License

This project is licensed under the GPL-3.0 license - see the [LICENSE](LICENSE) file for details.