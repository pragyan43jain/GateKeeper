import { defineConfig } from '@playwright/test'
export default defineConfig({
  testDir: './e2e',
  use: { baseURL: 'http://127.0.0.1:5189', headless: true, launchOptions: { executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH || undefined } },
  webServer: { command: 'npm run dev -- --host 127.0.0.1 --port 5189 --strictPort', url: 'http://127.0.0.1:5189', reuseExistingServer: !process.env.CI },
})
