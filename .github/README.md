# FilaBridge

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](https://www.gnu.org/licenses/gpl-3.0)
[![Go Version](https://img.shields.io/badge/Go-1.25%2B-00ADD8?logo=go)](https://go.dev/)
[![GitHub release](https://img.shields.io/github/v/release/needo37/filabridge)](https://github.com/needo37/filabridge/releases)

Manual release and container workflow runs publish the requested immutable
version tag. The rolling `latest` tag is updated only when that version is the
repository's highest stable SemVer; prereleases and older/backfilled tags cannot
roll the default image backward.

A high-performance Go microservice that bridges PrusaLink-compatible printers and Spoolman for (mostly) automatic filament inventory management. Originally designed for Prusa printers (CORE One, XL, MK4, etc.) but works with any printer that supports the PrusaLink API.

### The Problem

I run multiple 3D printers and use Spoolman to track my filament inventory. The issue? I had to manually update filament usage after every print. With multi-material prints on my Prusa XL, this was getting tedious and error-prone.

## Features

- 🔗 **PrusaLink Compatibility**: Works with any PrusaLink-compatible printer (Prusa CORE One, XL, MK4, Mini, and more)
- 📊 **Real-time Dashboard**: Web interface with live updates via WebSocket connections
- 🎯 **Multi-Toolhead Support**: Handles XL and CORE One/CORE One L INDX configurations up to 8 toolheads
- 📈 **Smart Usage Tracking**: Parses ASCII G-code and checksum-validated BGCode metadata per logical tool
- 💾 **Persistent Storage**: SQLite database stores toolhead mappings and complete print history
- ⚡ **High Performance**: Single lightweight binary, minimal resource usage, fast execution
- 🔧 **Web-based Config**: No config files needed - manage everything through the web UI
- 🔍 **Smart Spool Search**: Search and filter spools by ID, material, brand, or name with real-time filtering
- ⚠️ **Error Handling**: Print error detection with acknowledgment system for failed filament tracking
- 🔄 **Auto-mapping**: Automatic spool assignment when selecting from dropdown menus
- 🌐 **Live Updates**: Real-time status updates without page refreshes using WebSocket technology
- 🏷️ **NFC Tag Support**: Generate QR codes and program NFC tags for spools, filaments, and locations
- 📱 **Smart Scanning**: Two-step NFC workflow - scan spool + location (or location + spool) for instant assignment
- 📍 **Location Tracking**: Track spools in custom locations (dryboxes) or printer toolheads

## PrusaSlicer 3 and current firmware

FilaBridge supports PrusaSlicer 3.0's G-code metadata contract and its compressed BGCode container. Printer settings include current Prusa models plus CORE One and CORE One L/L+ INDX 4T/8T preview configurations. Logical slicer tools can be routed independently to physical material inputs.

PrusaLink connections accept hostnames or full HTTP/HTTPS URLs, API-key or Digest authentication, and custom CA certificates. Stored credentials are write-only in the API. `/api/version` diagnostics expose firmware and advertised capabilities. Persistent per-tool accounting retains prints through pause/attention/busy states, retries, and service restarts; stopped/error jobs are recorded without deducting planned filament. Ambiguous jobs are held for explicit `FINISHED` or `STOPPED` resolution through `/api/printers/:id/job-reconciliation` instead of being guessed.

OpenPrintTag remains capability-gated. Choose one consumption authority in Settings:

- `spoolman-led`: completed jobs update Spoolman automatically (default)
- `tag-led`: printer/tag owns consumption; FilaBridge records observations only
- `observed-only`: no automatic inventory mutation

Spoolman tag lookup and association are exposed only when its OpenAPI schema advertises them (`/api/spoolman/capabilities`, `/api/spoolman/tags/:uid`, and `/api/spools/:id/tags`). Tag-content synchronization remains disabled until released firmware and Spoolman APIs define that transport.

Compatibility baseline and source evidence: [PrusaSlicer 3.0 compatibility research](../docs/research/prusaslicer-3.0-filabridge-compatibility.md).

Spoolman filament definitions can also be synchronized into PrusaSlicer 3 as
FilaBridge-managed user profiles. This sync deliberately excludes physical
spools and remaining inventory. See [PrusaSlicer profile sync](../docs/prusaslicer-profile-sync.md).

## Why FilaBridge?

Managing filament inventory across multiple 3D printers is tedious. FilaBridge automates this by:

- Monitoring your printers in real-time with live WebSocket updates
- Tracking which spools are loaded on which toolheads
- Automatically updating your Spoolman inventory when prints complete
- Providing accurate filament usage by parsing G-code files
- Handling errors gracefully with clear notifications and acknowledgment system
- Using NFC tags to quickly assign spools to printers or storage locations
- Tracking filament locations across your workshop

No more manual updates or guesswork about remaining filament!

## Screenshots

![FilaBridge Dashboard](https://github.com/needo37/filabridge/blob/main/screenshots/dashboard.png?raw=true)
_FilaBridge main dashboard showing printer status and toolhead mappings_

![Spool Tags Management](https://github.com/needo37/filabridge/blob/main/screenshots/spool_tags.png?raw=true)
_NFC Management interface for generating QR codes for individual spools_

![Filament Tags Management](https://github.com/needo37/filabridge/blob/main/screenshots/filament_tags.png?raw=true)
_Filament type QR code generation for new unopened spools_

![Location Tags Management](https://github.com/needo37/filabridge/blob/main/screenshots/location_tags.png?raw=true)
_Location management interface for creating printer toolhead and storage location QR codes_

## Prerequisites

- A PrusaLink-compatible 3D printer (Prusa or any printer with PrusaLink API)
- PrusaLink enabled on your printer(s) for local network access
- Spoolman
- **For building from source**: Go 1.25 or higher
- **(Optional) For NFC features**: NFC-capable smartphone and NFC tags (NTAG213/215/216 recommended)
- **(Recommendation) NFC Tools Pro** mobile app (for programming tags)

## Installation

### Option 1: Docker (Easiest)

1. **Run Spoolman** (if not already running):

   ```bash
   docker run -d --name spoolman -p 8000:8000 -v spoolman-data:/home/spoolman/data ghcr.io/donkie/spoolman:latest
   ```

2. **Run FilaBridge**:

   ```bash
   docker run -d --name filabridge -p 5000:5000 \
     -e FILABRIDGE_WEB_USERNAME=admin \
     -e FILABRIDGE_WEB_PASSWORD='replace-with-a-long-random-password' \
     -v filabridge-data:/app/data \
     ghcr.io/needo37/filabridge:latest
   ```

3. **Configure**: Open `http://localhost:5000` and click "⚙️ Configuration"

**Using docker-compose (recommended for full stack):**

```bash
git clone https://github.com/needo37/filabridge.git
cd filabridge
export FILABRIDGE_WEB_USERNAME=admin
export FILABRIDGE_WEB_PASSWORD='replace-with-a-long-random-password'
docker compose up -d
```

The Compose file persists SQLite in the `filabridge-data` named volume. Access
from another device requires both web credential variables. Put FilaBridge
behind a TLS reverse proxy before sending Basic credentials across a network;
plain HTTP exposes those credentials to anyone able to observe that traffic.
For direct HTTP access, leave `FILABRIDGE_PUBLIC_ORIGIN` unset so the browser's
actual hostname or LAN address is accepted. When using a reverse proxy, set it
to the exact external origin, for example `https://filabridge.example.com`. FilaBridge uses it for
mutation/WebSocket origin checks and generated NFC URLs; do not include a path.

Existing installs using `./data:/app/data` can keep that bind mount, but the
directory must be writable by container UID/GID `10001` before switching to the
non-root image.

### Option 2: Pre-built Binary

1. **Download the latest release** for your platform from the [Releases page](https://github.com/needo37/filabridge/releases)
   - Linux (amd64, arm64)
   - macOS (amd64/Intel, arm64/Apple Silicon)
   - Windows (amd64)

2. **Make it executable** (Linux/macOS):

   ```bash
   chmod +x filabridge
   ```

3. **Run Spoolman** (if not already running):

   ```bash
   docker run -d --name spoolman -p 8000:8000 -v spoolman-data:/home/spoolman/data ghcr.io/donkie/spoolman:latest
   ```

4. **Start FilaBridge**:

   ```bash
   ./filabridge
   ```

5. **Configure**: Open `http://localhost:5000` and click "⚙️ Configuration"

### Option 3: Build from Source

1. **Clone and build**:

   ```bash
   git clone https://github.com/needo37/filabridge.git
   cd filabridge
   go mod download
   go build -o filabridge .
   ```

2. **Run Spoolman** (if not already running):

   ```bash
   docker run -d --name spoolman -p 8000:8000 -v spoolman-data:/home/spoolman/data ghcr.io/donkie/spoolman:latest
   ```

3. **Start FilaBridge**:
   ```bash
   ./filabridge
   ```

## Configuration

The system stores all configuration in the SQLite database. For Docker deployments, you can optionally set the `FILABRIDGE_DB_PATH` environment variable to specify where the database should be stored (defaults to `/app/data` in Docker).

### First Run

1. Start the application
2. Open the web interface at `http://localhost:5000`
3. Click "Start Configuration" button
4. Enter a name for your Printer.
5. Enter your PrusaLink IP Address and API key
6. Choose the number of toolheads your printer has.
7. Click "Save Configuration"
8. The service will automatically restart with new settings

## Usage

### Running the Service

```bash
# Run both bridge service and web interface (recommended)
./filabridge

# Custom host and port. Non-loopback access requires both credential variables.
FILABRIDGE_WEB_USERNAME=admin \
FILABRIDGE_WEB_PASSWORD='replace-with-a-long-random-password' \
./filabridge --host 0.0.0.0 --port 8080
```

### Web Interface

The web interface provides:

- **Printer Status**: Real-time view of printer states and current jobs with live WebSocket updates
- **Toolhead Mapping**: Assign filament spools to specific toolheads with smart search functionality
- **Progress Monitoring**: Visual progress bars for active prints
- **Live Updates**: Real-time status updates without page refreshes
- **Spool Search**: Search and filter spools by ID, material, brand, or name
- **Error Management**: View and acknowledge print processing errors
- **Auto-mapping**: Automatic spool assignment when selecting from dropdowns

### Filament Management

1. **Add spools to Spoolman**: Use Spoolman's web interface to add your filament spools
2. **Map spools to toolheads**: Use the FilaBridge web interface to assign spools with smart search
3. **Monitor usage**: The system automatically tracks and updates filament usage
4. **Handle errors**: Acknowledge any print processing errors that require manual intervention

### NFC Tag Management

1. **Generate QR Codes**: Navigate to NFC Management tab in the web interface
2. **Create Tags**:
   - **Spool Tags**: Generate QR codes for individual spools
   - **Filament Tags**: Generate QR codes for filament types (for new unopened spools)
   - **Location Tags**: Create and generate QR codes for printer toolheads and custom locations (dryboxes, storage shelves, etc.)
3. **Program NFC Tags**: Use NFC Tools Pro to scan QR codes and write URLs to NFC tags
4. **Assign Spools**: Tap spool tag, then location tag (location then spool works as well) to instantly assign and update inventory

## API Endpoints

The web interface also provides REST API endpoints:

All `/api/*` endpoints use the configured management Basic credentials. Only
`/healthz` remains unauthenticated for readiness probes.

- `GET /healthz` - Public process-readiness check (no printer or inventory data)
- `GET /api/status` - Get current printer status and mappings
- `GET /api/spools` - Get all spools from Spoolman
- `GET /api/prusaslicer/profiles.zip` - Download deterministic PrusaSlicer 3 managed filament profiles
- `POST /api/map_toolhead` - Map a spool to a toolhead
- `POST /api/unmap_toolhead` - Unmap a spool from a toolhead
- `GET /api/print-errors` - Get all unacknowledged print errors
- `POST /api/print-errors/{id}/acknowledge` - Acknowledge a print error
- `GET /api/nfc/assign` - Preview a scanned NFC tag and show confirmation
- `POST /api/nfc/assign` - Confirm the scan and update the NFC assignment session
- `GET /api/nfc/urls` - Get all NFC URLs with QR codes
- `GET /api/nfc/session/status` - Check NFC session status
- `GET /api/locations` - Get all locations
- `POST /api/locations` - Create custom location
- `PUT /api/locations/{name}` - Rename location
- `DELETE /api/locations/{name}` - Delete location
- `WS /ws/status` - WebSocket endpoint for real-time status updates

## Project Structure

```
filabridge/
├── main.go                 # Application entry point
├── config.go              # Configuration management
├── prusalink.go           # PrusaLink API client
├── spoolman.go            # Spoolman API client
├── bridge.go              # Core monitoring and tracking logic
├── nfc.go                 # NFC session management and tag handling
├── web.go                 # HTTP server and web interface
├── templates/             # HTML templates
├── go.mod                 # Go module definition
└── README.md              # Documentation
```

## Troubleshooting

### Common Issues

1. **Printers not accessible**:
   - Check IP addresses in the web interface configuration
   - Ensure PrusaLink is enabled on both printers
   - Verify network connectivity

2. **Spoolman connection failed**:
   - Make sure Spoolman is running
   - Check the Spoolman URL in the web interface configuration
   - Verify Spoolman is accessible at the specified URL

3. **Filament usage not tracked**:
   - Ensure spools are mapped to toolheads
   - Check that prints are completing (not just pausing)
   - Verify PrusaLink API is returning filament usage data

4. **WebSocket connection issues**:
   - Check browser console for WebSocket connection errors
   - Ensure no firewall is blocking WebSocket connections
   - The interface will fall back to periodic polling if WebSocket fails

5. **Print processing errors**:
   - Check the error notifications in the web interface
   - Acknowledge errors after manually updating Spoolman
   - Review logs for detailed error information

6. **NFC tag issues**:
   - Ensure NFC tags are NTAG213, NTAG215, or NTAG216 format
   - Use NFC Tools Pro to verify tag is properly formatted
   - QR codes encode the full URL - scan with NFC Tools Pro to program tags
   - Sessions expire after 5 minutes - complete both scans within the timeout

### Logs

The service logs important events to the console. Look for:

- Printer status updates
- Filament usage calculations
- Spoolman update confirmations
- WebSocket connection status
- Print processing errors
- Error messages

## Development

### Building from Source

```bash
# Download dependencies
go mod download

# Build the application
go build -o filabridge .

# Run tests
go test ./...

# Run the real-browser dashboard smoke test
npm ci --ignore-scripts --no-audit --no-fund
npx --no-install playwright install chromium
npm run test:browser

# Run with race detection
go run -race .
```

## Contributing

Contributions are welcome! Here's how you can help:

- 🐛 **Report bugs**: Open an issue with details about the problem
- 💡 **Suggest features**: Share your ideas for improvements
- 🔧 **Submit PRs**: Fix bugs or add features (please open an issue first for major changes)
- 📖 **Improve docs**: Help make the documentation clearer
- ⭐ **Star the repo**: Show your support!

See [CONTRIBUTING.md](CONTRIBUTING.md) for detailed guidelines.

## Roadmap

- [ ] Support for additional printer APIs
- [x] Provide a Docker Image
- [x] Real-time WebSocket updates
- [x] Enhanced spool search functionality
- [x] Print error handling and acknowledgment
- [x] NFC Support
- [ ] Mobile-responsive UI improvements

## Support the Project

If you find FilaBridge useful:

- ⭐ Star the repository
- 🐛 Report bugs and suggest features
- 📢 Share it with the 3D printing community
- 🤝 Contribute code or documentation

## License

This project is licensed under the GNU General Public License v3.0 - see the [LICENSE](LICENSE) file for details.

## Support

For issues specific to:

- **PrusaLink**: Check Prusa's documentation
- **Spoolman**: Visit the [Spoolman GitHub repository](https://github.com/pdrd/spoolman)
- **This bridge**: Open an issue in this repository
