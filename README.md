# pieceomoney

A personal Telegram bot that records Apple Pay transactions, breaks spending down by category and time, warns before you blow a monthly category limit, and exports everything to Excel.

It runs as a single bot and needs no public address: it long-polls Telegram, so a server behind NAT is fine.

```
Apple Pay tap
   -> iOS Shortcuts automation (Transaction trigger)
   -> "Log payment" shortcut -> Telegram Bot API (your bot token)
                                     | long polling
                                     v
                      pieceomoney (Go + SQLite) -> menus and alerts in your DM
```

> The bot's interface text (menus, messages) is in Russian. Code, comments and docs are in English.

## Features

- **Automatic capture** of every Apple Pay payment through one iOS automation.
- **Categories** attached to merchants: pick a category once and every payment from that merchant follows.
- **Overview** for today, week, month and previous month, with each category's share of spending.
- **Monthly limits per category** with progress, remaining amount per day and end-of-month pace. Alerts at 90% and again at 100%.
- **Time analytics**: spending by time of day and by weekday over the last 90 days.
- **Recent payments**: change a payment's category or delete it.
- **Excel export** of all transactions, plus a category-by-month summary sheet.
- **Owner-only**: the bot ignores everyone except your Telegram account.

## Requirements

- Go 1.27+ (or Docker)
- A Telegram bot token from @BotFather
- An iPhone with the Shortcuts app and Apple Pay

## Setup

1. Create a bot with @BotFather and copy its token.
2. Find your numeric Telegram user id (for example with @userinfobot).
3. Configure the environment:
   ```sh
   cp .env.example .env
   # set BOT_TOKEN and OWNER_USER_ID
   ```
4. Open your bot in Telegram and press **Start**. The bot cannot message you until you do.
5. Run it:
   ```sh
   make run            # local
   make compose-up     # Docker, restarts automatically
   ```

### Configuration

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `BOT_TOKEN` | yes | | Bot token from @BotFather |
| `OWNER_USER_ID` | yes | | Your numeric Telegram user id; the only account the bot talks to |
| `DEFAULT_CURRENCY` | no | `KZT` | Currency used for limits and analytics, and the fallback when a payment has none |
| `TZ_NAME` | no | `Asia/Almaty` | Timezone for displayed dates and day/week/month boundaries |
| `DB_PATH` | no | `data/money.db` | SQLite file |

## How payments reach the bot

A bot never receives updates for its own messages, so a plain `sendMessage` made with the bot token does not reach it. It does receive the final state of polls that it sent and then stopped. The shortcut uses this as a single-bot transport:

1. `sendPoll` with the payment as the poll question.
2. `stopPoll`, which makes Telegram deliver the poll to the bot.
3. `deleteMessage`, to keep the chat clean.

Only the holder of the bot token can create such a poll, so nobody else can inject payments. A poll question is limited to 300 characters; payments are far shorter.

## iPhone shortcut

Wallet gives the automation these properties: **Amount**, **Merchant**, **Card or Pass**, **Name** (no category and no date). The shortcut turns them into one line:

```
amount|merchant|card|category[|currency[|date]]
```

For example `KZT 350.00|Love Is Coffee|Freedom Deposit Card|`. The category is left empty on purpose; the bot assigns it (see Categories). The bot reads the currency from the amount (`KZT 350.00`, `USD 12.50`, `$5`, `₸2,500`) and uses the time it receives the payment as the payment time.

### 1. Create the shortcut "Log payment"

Shortcuts app, Shortcuts tab, `+`, name it `Log payment`, then add these actions in order:

1. **Receive** input: leave the default. Set "If there's no input" to **Continue**.
2. **Text**: `[Amount]|[Merchant]|[Card or Pass]|`. Insert each variable by tapping the field, choosing **Shortcut Input**, tapping the inserted variable and choosing the property. Keep the trailing `|`.
3. **Get Contents of URL**
   - URL: `https://api.telegram.org/bot<TOKEN>/sendPoll`
   - Show More: Method **POST**, Request Body **Form**
   - Fields (all Text): `chat_id` = your user id, `question` = the **Text** variable from step 2, `options` = `["a","b"]`
4. **Get Dictionary Value**: get **Value** for `result` in **Contents of URL**.
5. **Get Dictionary Value**: get **Value** for `message_id` in **Dictionary Value**.
6. **Get Contents of URL**: `.../bot<TOKEN>/stopPoll`, POST, Form, fields `chat_id` and `message_id` (the **Dictionary Value** from step 5). This step delivers the payment to the bot.
7. **Get Contents of URL**: `.../bot<TOKEN>/deleteMessage`, POST, Form, the same two fields.

### 2. Create the automation

Automation tab, `+`, **Transaction**:

1. Select all your cards and all categories.
2. Choose **Run Immediately** and turn **Notify When Run** off.
3. In "Do", choose the `Log payment` shortcut.

The automation passes the Wallet transaction to the shortcut as its input.

### Notes

- Running the shortcut by hand sends empty properties, so the bot replies with a parse error that shows the text it received. This is expected and useful for debugging. Use a real payment to test the full path.
- Two firings of the same payment within a minute are stored once.
- If the Mac or server running the bot is asleep, Telegram keeps updates for 24 hours and the bot processes them when it wakes up.

## Categories

Wallet does not expose a category, so the bot learns it per merchant:

- A payment from an unknown merchant arrives as **Uncategorized** with a "Category" button.
- Pick a category once. The bot remembers it for that merchant (case-insensitive name match) and applies it to all future payments there, and to that merchant's past payments that are still uncategorized. Payments you categorized yourself are never overwritten.
- Apple's category names are offered by default (Food & Drinks, Shopping, Travel, Services, Entertainment, Health) along with any you have used.
- You can also type `/cat <merchant> = <category>`.

## Using the bot

Everything is driven by inline buttons that edit one message in place, plus a persistent bottom keyboard.

| Screen | What it shows |
| --- | --- |
| Overview | Totals for today / week / month / previous month, category shares, comparison with last month |
| Categories | Spending per category, drill into top merchants |
| Limits | Monthly limit per category: progress bar, remaining per day, projected end-of-month spend |
| Time | Spending by time of day and weekday (90 days) |
| Recent | Last payments; change category or delete |
| Excel | Sends an `.xlsx` with all transactions |

Limits and analytics count payments in `DEFAULT_CURRENCY`; other currencies appear as a separate line in the overview. Limit alerts fire once at 90% and once at 100% as a payment crosses each threshold.

### Commands

| Command | Action |
| --- | --- |
| `/start` | Welcome, enable the bottom keyboard, open the menu |
| `/menu` | Main menu |
| `/stats` | Overview |
| `/budget` | Limits; `/budget <category> <amount>` sets one, `/budget <category> off` removes it |
| `/last` | Recent payments |
| `/time` | Time analytics |
| `/export` | Excel export |
| `/cat <merchant> = <category>` | Remember a merchant's category |

### Excel export

The workbook has two sheets: all transactions (real date, time and number cells, frozen header, filters) and a category-by-month summary in `DEFAULT_CURRENCY`.

## Security

- Only a private chat with `OWNER_USER_ID` is accepted; every other message and button press is ignored and logged.
- The bot sends data only to that chat.
- Anyone holding the bot token can post payments (via polls) and can read the token out of your shortcut. Keep both private, and rotate the token with `/revoke` in @BotFather if it leaks (then update `.env` and the shortcut).
- `.env` and `data/` are gitignored.

## Development

```sh
make test      # go test ./...
make build     # bin/pieceomoney
```

Layout:

```
cmd/server                      entry point
internal/app.go                 wiring, long polling, command menu
internal/core/cfg               environment configuration
internal/pkg/ledger/svc         payload parsing, amount and currency detection, formatting
internal/pkg/storage/repo       SQLite schema and repositories
internal/pkg/telegram/svc       update handling, screens, commands, budget alerts
internal/pkg/export/svc         Excel export
```

Money is stored as integer minor units. A fingerprint over the payment fields makes duplicate firings idempotent.
