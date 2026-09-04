const os = require('node:os');
const path = require('node:path');
const fs = require('node:fs');
const { defineConfig } = require('@playwright/test');

const port = 15119;
const databaseDirectory = path.join(os.tmpdir(), `filabridge-browser-${process.pid}`);
fs.mkdirSync(databaseDirectory, { recursive: true });

module.exports = defineConfig({
  testDir: './tests/browser',
  fullyParallel: false,
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    baseURL: `http://127.0.0.1:${port}`,
    browserName: 'chromium',
  },
  webServer: {
    command: `go run . --web-only --host 127.0.0.1 --port ${port}`,
    env: {
      ...process.env,
      FILABRIDGE_DB_PATH: databaseDirectory,
    },
    url: `http://127.0.0.1:${port}/healthz`,
    reuseExistingServer: false,
    timeout: 120_000,
  },
});
