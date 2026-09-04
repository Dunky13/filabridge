const { test, expect } = require('@playwright/test');

test('dashboard loads, reports health, and renders configuration data as text', async ({ page, request }) => {
  const browserErrors = [];
  page.on('pageerror', error => browserErrors.push(error.message));

  const maliciousUsername = '\"><img id="xss-marker" src=x onerror="window.__xss=true">';
  await page.route('**/api/config', route => route.fulfill({
    contentType: 'application/json',
    body: JSON.stringify({
      spoolman_url: 'http://127.0.0.1:8000',
      spoolman_username: maliciousUsername,
      spoolman_password_configured: true,
      poll_interval: 30,
      consumption_authority: 'spoolman-led',
    }),
  }));

  await page.goto('/');
  await expect(page.getByRole('heading', { name: /FilaBridge Dashboard/ })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Printer Status' })).toBeVisible();

  await page.getByRole('button', { name: /Settings/ }).click();
  await page.getByRole('button', { name: /Basic Configuration/ }).click();
  await expect(page.locator('#spoolman_username')).toHaveValue(maliciousUsername);
  await expect(page.locator('#spoolman_password')).toHaveValue('');
  await expect(page.locator('#spoolman_password_status')).toContainText('A password is configured');
  await expect(page.locator('#xss-marker')).toHaveCount(0);
  expect(await page.evaluate(() => window.__xss)).toBeUndefined();

  const health = await request.get('/healthz');
  expect(health.ok()).toBeTruthy();
  await expect(health.json()).resolves.toMatchObject({ status: 'ok' });
  expect(browserErrors).toEqual([]);
});
