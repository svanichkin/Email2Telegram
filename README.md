# Email2Telegram

**Email2Telegram** is a powerful and flexible tool that bridges your email inbox with your Telegram account. It allows you to receive new emails directly in a Telegram chat, reply to them, and even compose new emails—all from the convenience of Telegram.

## Features

*   **Email Forwarding:** Automatically forwards incoming emails from your IMAP account to your specified Telegram chat.
*   **Reply from Telegram:** Easily reply to emails using Telegram's native reply feature.
*   **Compose New Emails:** Send new emails directly from Telegram by messaging your bot.
*   **Attachment Support:** Handles both incoming and outgoing email attachments (documents, images, etc.).
*   **Real-time Notifications:** Utilizes IMAP IDLE for instant notifications of new emails, ensuring you stay updated.
*   **Secure Credential Storage:** Protects your email credentials by storing them in your system's keyring (e.g., macOS Keychain, GNOME Keyring, Windows Credential Manager) with a fallback to an AES-256 encrypted file if keyring access is unavailable.
*   **Configuration File:** Simple and clear configuration via an `email2telegram.conf` file.
*   **Cross-Platform:** Pre-compiled binaries are available for Linux, macOS, and Windows (amd64 & arm64 architectures) via GitHub Releases.
*   **HTML Email Handling:** Parses HTML emails and attempts to convert them to Telegram-friendly HTML formatting.
*   **Graceful Shutdown:** Handles termination signals cleanly.
*   **Single User Focus:** Designed for a single user to securely manage their email via a private Telegram bot.

## How it Works

Email2Telegram performs the following main operations:

1.  **Connects to IMAP:** Establishes a secure connection to your email provider's IMAP server using the credentials you provide.
2.  **Listens for Emails:** Monitors your inbox for new emails, primarily using the IMAP IDLE command for efficiency.
3.  **Parses Emails:** When a new email arrives, it fetches and parses its content, including sender, recipients, subject, body (text and HTML), and attachments.
4.  **Forwards to Telegram:** Sends a formatted message to your Telegram chat via your private Telegram bot. Attachments are sent as separate documents.
5.  **Handles Telegram Commands:**
    *   **Replies:** When you reply to an email message in Telegram, the bot constructs an email reply and sends it via SMTP.
    *   **New Emails:** When you send a specially formatted message to the bot, it composes and sends a new email via SMTP.

## Installation and Setup

### 1. Download a Release

Pre-compiled binaries for Linux, macOS, and Windows are available on the project's **GitHub Releases page**. Please navigate to the releases section of this repository to download the appropriate archive for your operating system and architecture. Extract the `email2telegram` executable.

### 2. Initial Configuration

Place the `email2telegram` executable in a directory of your choice.

When you run `email2telegram` for the first time, or if the configuration is incomplete, it will guide you through the setup process:

*   **Telegram Bot Token:**
    *   You'll need a Telegram Bot Token. Create a new bot by talking to the [BotFather](https://t.me/BotFather) on Telegram.
    *   Follow its instructions and copy the token it provides.
*   **Telegram User ID:**
    *   This is your personal, numeric Telegram User ID. The bot will only respond to this user.
    *   You can get your User ID by sending `/start` to a bot like [IDBot](https://t.me/myidbot) or [@userinfobot](https://t.me/userinfobot).
*   **Email Credentials:**
    *   The application will prompt you to enter your email address and password.
    *   These credentials are required to connect to your IMAP and SMTP servers.
    *   **Security:** Your credentials will be stored securely in your system's native keyring. If keyring access fails, they will be stored in an AES-256 encrypted file named `<YourTelegramUserID>.key` in the same directory as the executable.

### 3. The `email2telegram.conf` File

After the initial setup, or if you prefer to configure manually, a configuration file named `email2telegram.conf` will be created in the same directory as the executable. It looks like this:

```ini
[telegram]
token = YOUR_TELEGRAM_BOT_TOKEN
user_id = YOUR_TELEGRAM_USER_ID

[email]
imap_host = imap.example.com
imap_port = 993
smtp_host = smtp.example.com
smtp_port = 587
# Optional: If your IMAP and SMTP host are the same and use standard ports,
# you might only need to specify your email and password during the interactive setup.
# The application will try to derive hosts from your email address.
# username = your_email@example.com # Can be set here or entered interactively
```

**Configuration Options:**

*   **`[telegram]`**
    *   `token`: Your Telegram bot's API token.
    *   `user_id`: Your numeric Telegram user ID. The bot will only interact with this user.
*   **`[email]`**
    *   `imap_host`: (Optional) Your IMAP server hostname (e.g., `imap.gmail.com`). If left blank, the application will try to derive it from your email domain.
    *   `imap_port`: (Optional) Your IMAP server port. Defaults to `993` (for IMAP over SSL/TLS).
    *   `smtp_host`: (Optional) Your SMTP server hostname (e.g., `smtp.gmail.com`). If left blank, the application will try to derive it from your email domain.
    *   `smtp_port`: (Optional) Your SMTP server port. Defaults to `587` (for SMTP with STARTTLS).
    *   `username`: (Optional) Your full email address. If not provided here, and not found in the keyring from a previous run, you will be prompted for it when the application starts.

**Note on Email Providers (Gmail, Outlook, etc.):**
*   You might need to enable IMAP access in your email account settings.
*   For services like Gmail or Outlook that use OAuth2 or have strong security defaults, you may need to generate an "App Password" to use with Email2Telegram instead of your regular account password.

## Usage

1.  **Run the Application:** Execute `./email2telegram` (or `email2telegram.exe` on Windows) from your terminal in the directory where it's located.
2.  **Receiving Emails:** New emails will automatically appear as messages in your Telegram chat with the bot. They will include the subject, sender, recipient(s), body, and any attachments as separate files.
3.  **Replying to Emails:**
    *   Use Telegram's "Reply" feature on the message containing the email you want to reply to.
    *   Type your reply message.
    *   You can attach files/photos/videos to your reply; they will be sent as email attachments.
4.  **Sending New Emails:**
    *   Send a message directly to your bot in the following format:
        ```
        recipient@example.com
        Subject Line Here
        The body of your new email goes here.
        It can span multiple lines.
        ```
    *   To include attachments, send the text message above first. Then, send the files (photos, documents, etc.) as separate messages immediately after. The application is designed to group media sent in quick succession. If you caption a file, ensure the main email text (recipient, subject, body) is sent as a distinct message.

## Contributing

Contributions are welcome! Here are some ways you can help:

*   **Reporting Bugs:** If you find a bug, please open an issue on the GitHub repository, providing as much detail as possible (console output, steps to reproduce, email provider if relevant).
*   **Suggesting Enhancements:** Have an idea for a new feature or an improvement? Open an issue to discuss it.
*   **Pull Requests:** If you'd like to contribute code:
    1.  Fork the repository.
    2.  Create a new branch for your feature or bug fix (`git checkout -b my-new-feature`).
    3.  Make your changes.
    4.  Add tests for your changes. (Currently, the project lacks a comprehensive test suite, which is a key area for improvement!)
    5.  Ensure your code is well-formatted (`go fmt ./...`) and passes linting checks (`golangci-lint run` if a configuration is provided).
    6.  Submit a pull request with a clear description of your changes.

## License

This project is licensed under the terms of the MIT License. See the `LICENSE` file for details.
