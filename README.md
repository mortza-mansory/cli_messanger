# Cli Messenger
A simple CLI messenger client and backend (server) using tcell/tview and HTTP Long Polling for maximum safety in private messaging. Bring secure messaging to your terminal from 2000 to 2026.

## Why This Project?
Most messaging apps today are too complicated. They use WebSockets (which can be hacked), store your messages forever, and know too much about you. This project is different:

- **No WebSockets** → Just simple HTTP
- **No databases** → Messages live in memory for 1 minute, then disappear
- **No message history** → After 1 minute, it's gone forever
- **No user accounts** → Just a username and a color

## Usage Policy
This project is for learning purposes. While it works, I am not responsible for any usage that isn't educational or related to my learning curve. 

If you use this for something serious, that's on you. I built this to learn Go, HTTP, and terminal UIs.

## Features

### 🔐 Security First
- **End-to-End Encryption** - Messages are encrypted on your device. The server only sees random text. Even if someone hacks the server, they can't read your messages.
- **Access Key Protection** - Only clients with the secret key can connect. Your server stays private.
- **No Message Storage** - Messages live in RAM for 60 seconds, then disappear forever. No hard drive, no database, no traces.

### 📡 Smart Communication
- **HTTP Long Polling** - Instead of WebSockets (which can be attacked), we use simple HTTP. Your client asks "any new messages?" and the server waits 30 seconds before saying "no". When a message arrives, the server answers immediately.
- **Works Everywhere** - HTTP works through any firewall, any proxy, any network. No special setup needed.
- **Low Bandwidth** - One connection every 30 seconds. Perfect for slow internet.

### 🎨 Terminal UI (TUI)
- **Beautiful Colors** - Messages come with colors like [red], [green], [yellow], [blue]. Your terminal becomes a colorful chat room.
- **Clean Interface** - Built with tcell/tview. No mouse needed, just keyboard.
- **Lightweight** - Runs on anything. Old laptop? Raspberry Pi? Termux on Android? Yes, yes, yes.

### ⚡ Performance
- **10 Concurrent Users** - Designed for small groups. Family, friends, study group.
- **1000 Messages in Memory** - With 10 users sending 10 messages per second, that's 100 messages/second. Our 1000 message buffer gives you 10 seconds of history.
- **1 Minute TTL** - Messages auto-delete after 60 seconds. Perfect for quick conversations.

### 🛡️ Anti-Hacking
- **Rate Limiting** - 10 messages per second per user. No spam, no flooding.
- **Auto Cleanup** - Inactive users are removed after 24 hours.
- **No WebSockets** - Removes a whole category of attacks.
- **In-Memory Only** - Nothing written to disk. Pull the power plug, and all messages are gone.

## How It Works

### The Big Picture
```
┌─────────┐     ┌─────────┐     ┌─────────┐
│ Client  │────▶│ Server  │────▶│ Client  │
│    A    │◀────│ (Brain) │◀────│    B    │
└─────────┘     └─────────┘     └─────────┘
                      │
                      ▼
                 ┌─────────┐
                 │ Client  │
                 │    C    │
                 └─────────┘
```

### Step by Step

**1. Client Starts**
- User enters a username (like "script_kiddie")
- User picks a color (like "[yellow]")
- Client connects to server with secret access key

**2. Sending a Message**
```
Client A → Server → (stores in RAM) → "I have a message for everyone"
```
The message is encrypted before sending. Server sees: "U2FsdGVkX1+6nJqZv..."

**3. Waiting for Messages**
```
Client B → Server → "Any messages for me?"
Server → (waits 30 seconds) → "No"
```
But if a message arrives during those 30 seconds:
```
Client A sends message
Server → Client B → "Here's the new message!"
Client B asks again immediately
```

**4. Message Lifetime**
- Message created at 12:00:00
- Available until 12:01:00
- At 12:01:01, it's gone forever

## Technical Architecture

### Backend (Go)
```
internal/
├── models/
│   ├── message.go        # What a message looks like
│   └── buffer.go         # In-memory message storage
├── services/
│   ├── chat_service.go   # Send/receive logic
│   └── auth_service.go   # Access keys + rate limiting
├── controllers/
│   ├── send_controller.go    # POST /api/send
│   ├── poll_controller.go    # GET /api/poll
│   └── stats_controller.go   # GET /api/stats
└── middleware/
    ├── logging.go        # Logs every request
    ├── recovery.go       # Catches crashes
    └── cors.go           # Allows browser testing
```


```

## API Reference

### Send a Message
```http
POST /api/send
Content-Type: application/json

{
    "access_key": "your_secret_key",
    "client_id": "unique_client_id",
    "username": "script_kiddie",
    "content": "Anyone using Go 1.22 yet?",
    "color": "[yellow]"
}
```

**Response:**
```json
{
    "status": "sent",
    "id": "msg_1700000000_42",
    "time": "2024-01-01T12:00:00Z"
}
```

### Get New Messages (Long Polling)
```http
GET /api/poll?access_key=your_secret_key&client_id=unique_id&last_id=msg_1700000000_42
```

**Response (when messages arrive):**
```json
[
    {
        "script_kiddie": "Anyone using Go 1.22 yet?",
        "color": "[yellow]",
        "id": "msg_1700000000_42",
        "timestamp": "2024-01-01T12:00:00Z"
    },
    {
        "h4x0r": "Still on 1.21, waiting for generics to stabilize",
        "color": "[red]",
        "id": "msg_1700000001_43",
        "timestamp": "2024-01-01T12:00:05Z"
    }
]
```

**Response (timeout - no messages):**
```
HTTP 204 No Content
```

### Server Stats
```http
GET /api/stats
```

**Response:**
```json
{
    "chat_stats": {
        "total_messages": 42,
        "waiting_clients": 3,
        "max_waiters": 1000
    },
    "active_clients": 5,
    "status": "running"
}
```

## Installation

### Prerequisites
- Go 1.21 or higher
- Git


**Android (Termux):**
```bash
pkg install golang
go build -o client cmd/client/main.go
./client
```

## Configuration

### Command Line Flags (Server)
| Flag | Default | Description |
|------|---------|-------------|
| `-port` | `8034` | Port to listen on |
| `-key` | `secure_chat_key_2024` | Access key for clients |
| `-max-msgs` | `1000` | Max messages in memory |
| `-ttl` | `1m` | How long messages live |

### Command Line Flags (Client)
| Flag | Default | Description |
|------|---------|-------------|
| `-server` | `http://localhost:8034` | Server address |
| `-key` | `secure_chat_key_2024` | Access key |
| `-username` | Random | Your display name |
| `-color` | `[white]` | Your message color |

## Security Deep Dive

### Why No WebSockets?
WebSockets are great for real-time chat, but:
1. They keep connections open forever (容易受到 DoS 攻击)
2. Some corporate firewalls block them
3. They're harder to secure properly
4. More attack surface for hackers

Long Polling is simpler, works everywhere, and is easier to make secure.

### Encryption (End-to-End)
```go
// On the client:
plaintext := "Hello world!"
encrypted := aesGCM.Encrypt(plaintext, userKey)
// Send encrypted text to server

// Server sees: "��@���x��K�Ʊ���" (meaningless)
// Server stores this for 60 seconds

// Other clients receive encrypted text
decrypted := aesGCM.Decrypt(encrypted, userKey)
// Display: "Hello world!"
```

Even if someone steals the server's memory, they just see random bytes.

### Access Key Protection
The access key is shared between all clients. Think of it like a Wi-Fi password:
- Only people with the password can join
- Change it if someone leaves
- Keep it secret, keep it safe

### Rate Limiting
Each client can send:
- **10 messages per second** (burst limit)
- **20 messages in a row** (then wait)

This stops spam and DoS attacks.

## Message Format Examples

### What the Client Sends
```json
{
    "access_key": "family_chat_2024",
    "client_id": "alice_laptop",
    "username": "alice",
    "content": "Hey everyone, what's for dinner?",
    "color": "[blue]"
}
```

### What Other Clients See
```json
[
    {
        "alice": "Hey everyone, what's for dinner?",
        "color": "[blue]",
        "id": "msg_123456",
        "timestamp": "2024-01-01T18:30:00Z"
    }
]
```

### Display in Terminal
```
[blue]alice: Hey everyone, what's for dinner?
[yellow]bob: Pizza!
[red]charlie: I'm vegetarian, can we do something else?
[green]dave: How about pasta?
```

## Use Cases

### 1. Family Group
- 5 family members
- Quick messages about dinner, plans, etc.
- Messages disappear after 1 minute - no awkward history

### 2. Study Group
- 4 students
- Share quick questions and answers
- No registration, no emails, just join and chat

### 3. Office Team (Small)
- 8 coworkers
- Quick updates without Slack noise
- Messages auto-delete - perfect for sensitive info

### 4. Terminal Lovers
- People who live in the terminal
- No need to switch to browser or phone
- Just alt-tab to terminal and type

## Limitations (Honest Talk)

### What This Project CAN'T Do
- ❌ No file sharing (text only)
- ❌ No private messages (everyone sees everything)
- ❌ No message history (gone after 1 minute)
- ❌ No user accounts (just usernames)
- ❌ No mobile app (Termux only for Android)
- ❌ No encryption key rotation (same key forever)

### What This Project CAN Do
- ✅ Simple, secure group chat
- ✅ Educational code to learn from
- ✅ Runs anywhere Go runs
- ✅ Easy to modify and extend
- ✅ Privacy-focused design

## Troubleshooting

### "Port already in use"
```bash
# Find what's using the port (Windows)
netstat -ano | findstr :8034
taskkill /PID <PID> /F

# Linux/macOS
lsof -i :8034
kill -9 <PID>
```

### "Connection refused"
- Is the server running?
- Check the port number
- Firewall? Try `http://localhost:8034` first

### Messages not appearing
- Check access key (must match server)
- Check client ID (should be unique)
- Look at server logs for errors

## Contributing

This is a learning project, but contributions are welcome:

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Test thoroughly
5. Submit a pull request

### Ideas for Improvement
- Private messaging between users
- Multiple chat rooms
- Better encryption (key rotation)
- File sharing (encode in base64)
- Mobile app (Flutter maybe)

## License
MIT License - Do whatever you want, but don't blame me if something breaks.

## Final Words
This project started as "I wonder if I can make a terminal chat app" and became "Hey, this actually works pretty well". It's not perfect, it's not for millions of users, but for a small group of terminal lovers, it's just right.

If you learn something from this code, my job is done. If you actually use it for real conversations, that's cool too. Just remember: messages disappear after 1 minute. Don't send anything you need to remember!

---

**Happy chatting from your terminal!** 📟
```
