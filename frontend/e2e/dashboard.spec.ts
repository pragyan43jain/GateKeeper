import { test, expect } from '@playwright/test'
// Deterministic UI fixtures only. Real gateway authorization is covered by Go integration tests.
test('mobile session restoration, drawer keyboard access, navigation and logout', async ({ page }) => {
  let signedIn = false
  const user = { id: 'ui-test', email: 'viewer@example.test', role: 'viewer' }
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (path === '/api/auth/setup') return route.fulfill({ json: { setupRequired: false } })
    if (path === '/api/auth/login') { signedIn = true; return route.fulfill({ json: user }) }
    if (path === '/api/auth/logout') { signedIn = false; return route.fulfill({ status: 204 }) }
    if (path === '/api/auth/me') return route.fulfill({ status: signedIn ? 200 : 401, json: signedIn ? user : {} })
    if (path === '/api/metrics') return route.fulfill({ status: signedIn ? 200 : 401, body: 'gateway_requests_total{code="200"} 2\n' })
    return route.fulfill({ json: { backends: [], requests: [], users: [] } })
  })
  await page.route('**/health', route => route.fulfill({ body: 'Gatekeeper is healthy' }))
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/')
  await page.getByLabel('Email address').fill(user.email)
  await page.getByLabel('Password', { exact: true }).fill('ui-test-password')
  await page.locator('form').getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('button', { name: 'Open navigation' })).toBeVisible()
  await page.reload()
  await expect(page.getByRole('button', { name: 'Open navigation' })).toBeVisible()
  await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeHidden()
  await page.getByRole('button', { name: 'Open navigation' }).click()
  await expect(page.getByRole('navigation', { name: 'Main navigation' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('button', { name: 'Open navigation' })).toBeFocused()
  await page.getByRole('button', { name: 'Open navigation' }).click()
  await page.getByRole('button', { name: 'Settings & users' }).click()
  await expect(page.getByText('Profile & security')).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBeTruthy()
  await page.getByRole('button', { name: 'Account menu' }).click()
  await page.getByRole('button', { name: 'Sign out', exact: true }).click()
  await expect(page.getByText('You have been signed out securely.')).toBeVisible()
  await page.reload()
  await expect(page.getByLabel('Email address')).toBeVisible()
})

test('failed setup connection can be retried', async ({ page }) => {
  let failed = true
  await page.route('**/api/auth/me', route => route.fulfill({ status: 401 }))
  await page.route('**/api/auth/setup', route => failed ? route.fulfill({ status: 503 }) : route.fulfill({ json: { setupRequired: false } }))
  await page.goto('/')
  await expect(page.getByRole('button', { name: 'Retry gateway connection' })).toBeVisible()
  failed = false
  await page.getByRole('button', { name: 'Retry gateway connection' }).click()
  await expect(page.locator('form').getByRole('button', { name: 'Sign in' })).toBeEnabled()
})
