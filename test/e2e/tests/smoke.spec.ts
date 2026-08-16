import { test, expect } from '@playwright/test';

const FAKES = process.env.FAKES_URL ?? 'http://localhost:9090';

test('dashboard renders seeded attention items and repo cards', async ({ page }) => {
  await page.goto('/');
  await expect(page).toHaveTitle(/zorgscope/);
  const attention = page.locator('#tile-attention');
  await expect(attention).toBeVisible();
  // first fetch happens right after start; the tile polls every 5 s
  await expect(attention.getByText('Add example stakeholder table')).toBeVisible({ timeout: 30_000 });
  await expect(attention.locator('.badge-unanswered').first()).toBeVisible();
  await expect(attention.locator('.badge-new').first()).toBeVisible();      // #240 opened 2 h ago
  await expect(attention.locator('.badge-build_failed')).toHaveCount(1);    // arc42.org-site deploy failed
  const repos = page.locator('#tile-repos');
  await expect(repos.getByText('arc42-template')).toBeVisible();
  await expect(repos.locator('.build-failed')).toHaveCount(1);
  await expect(page.locator('#tile-header .source-ok').first()).toBeVisible();
});

test('a newly opened issue appears with NEW after refresh (QS-1.1)', async ({ page, request }) => {
  await page.goto('/');
  await expect(page.locator('#tile-attention .row').first()).toBeVisible({ timeout: 30_000 });
  const now = new Date().toISOString();
  const res = await request.post(`${FAKES}/__control/issues`, {
    data: { repo: 'arc42/arc42-template', issue: { Number: 4711, Title: 'Freshly opened by contributor', Author: 'contributor', CreatedAt: now, UpdatedAt: now } },
  });
  expect(res.status()).toBe(204);
  await page.getByRole('button', { name: /refresh/ }).click();
  const row = page.locator('#tile-attention .row', { hasText: 'Freshly opened by contributor' });
  await expect(row).toBeVisible({ timeout: 30_000 });
  await expect(row.locator('.badge')).toHaveText('NEW');
});

test('dismiss removes an item and survives reload (FR-2.7)', async ({ page }) => {
  await page.goto('/');
  const row = page.locator('#tile-attention .row', { hasText: 'Add example stakeholder table' });
  await expect(row).toBeVisible({ timeout: 30_000 });
  await row.getByRole('button', { name: /dismiss/ }).click();
  await expect(page.locator('#tile-attention .row', { hasText: 'Add example stakeholder table' })).toHaveCount(0);
  await page.reload();
  await expect(page.locator('#tile-attention .row', { hasText: 'Add example stakeholder table' })).toHaveCount(0);
});

test('fits a phone viewport without horizontal scroll (QS-5.2)', async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 800 });
  await page.goto('/');
  await expect(page.locator('#tile-attention')).toBeVisible();
  const scrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
  expect(scrollWidth).toBeLessThanOrEqual(375);
});
