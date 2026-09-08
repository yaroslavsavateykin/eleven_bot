// Run with PLAYWRIGHT_MODULE=/path/to/playwright-core/index.mjs CHROMIUM_PATH=/path/to/chrome node internal/web/static/browser.test.mjs
import assert from 'node:assert/strict';
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright-core');
const browser = await chromium.launch({ executablePath: process.env.CHROMIUM_PATH, headless: true, args: ['--no-sandbox'] });
try {
  for (const width of [320, 494, 1280]) {
    const page = await browser.newPage({ viewport: { width, height: 850 } });
    const errors = [];
    page.on('pageerror', error => errors.push(error.message));
    await page.clock.install({ time: new Date('2026-09-08T08:00:00Z') });
    const lesson = { id: 7, kind: 'lesson', category: 'lesson', title: '<img src=x onerror=alert(1)> Математика', starts_at: '2026-09-08T07:59:30Z', ends_at: '2026-09-08T08:01:00Z' };
    let current = lesson;
    await page.route('**/api/public/v1/status', route => route.fulfill({ json: { current } }));
    await page.route('**/api/public/v1/schedule?*', route => route.fulfill({ json: { events: [lesson,
      { ...lesson, id: 8 }, { ...lesson, starts_at: '2026-09-09T07:59:30Z', ends_at: '2026-09-09T08:01:00Z' },
    ] } }));
    await page.goto('http://127.0.0.1:8080/');
    const ready = () => page.waitForFunction(() => document.querySelector('#schedule').getAttribute('aria-busy') === 'false');
    await ready();
     assert.equal(await page.locator('#dates button').count(), 7);
     assert.equal(await page.locator('.active-lesson').count(), 1);
     assert.equal(await page.locator('#agenda img, #status, .now').count(), 0);
      assert.deepEqual(await page.locator('nav button').allTextContents(), ['←', 'Неделя', 'Месяц', '→']);
      assert.equal(await page.locator('#previous').getAttribute('aria-label'), 'Предыдущая');
      assert.equal(await page.locator('#next').getAttribute('aria-label'), 'Следующая');
      assert.equal(await page.locator('#previous').getAttribute('title'), 'Предыдущая');
      assert.equal(await page.locator('#next').getAttribute('title'), 'Следующая');
      assert.ok(await page.locator('#previous span, #next span').evaluateAll(elements => elements.every(el => el.getAttribute('aria-hidden') === 'true')));
     const toolbar = () => page.locator('nav button').evaluateAll(elements => elements.map(el => {
      const r = el.getBoundingClientRect(); return { id: el.id || el.dataset.view, x: r.x, right: r.right, center: r.y + r.height / 2 };
    }));
    const toolbarPositions = await toolbar();
    assert.ok(toolbarPositions.every(box => Math.abs(box.center - toolbarPositions[0].center) < 1));
    assert.deepEqual(toolbarPositions.map(box => box.id), ['previous', 'week', 'month', 'next']);
    assert.ok(toolbarPositions.every((box, i) => !i || box.x >= toolbarPositions[i - 1].right));
    assert.ok(toolbarPositions[1].x - toolbarPositions[0].right <= 8);
    assert.ok(toolbarPositions[3].x - toolbarPositions[2].right <= 8);
    const caption = await page.locator('#period-title').boundingBox();
    const selector = await page.locator('#view').boundingBox();
    assert.ok(caption.y + caption.height <= selector.y);
     assert.ok(Math.abs(caption.x + caption.width / 2 - selector.x - selector.width / 2) < 1);
      const checkAlignment = async () => {
         const boxes = await page.locator('.calendar, header, #period-title, .period-bar, #view').evaluateAll(elements =>
         Object.fromEntries(elements.map(el => {
           const r = el.getBoundingClientRect();
           return [el.id || el.className || el.tagName.toLowerCase(), { x: r.x, y: r.y, right: r.right, bottom: r.bottom, center: r.x + r.width / 2 }];
         })));
         for (const name of ['header', 'period-title', 'period-bar', 'view']) {
           assert.ok(Math.abs(boxes[name].center - boxes.calendar.center) <= 1, `${width}px: ${name} centered on calendar`);
         }
         assert.deepEqual(await toolbar(), toolbarPositions);
      };
      await checkAlignment();
    assert.equal(await page.locator('nav').getAttribute('aria-describedby'), 'period-title');
    for (const control of await page.locator('#view, #view button').all()) {
      assert.equal(await control.evaluate(el => getComputedStyle(el).borderTopWidth), '0px');
    }
    const weekCaption = await page.locator('#period-title').innerText();
    await page.locator('#previous').click();
    await ready();
     assert.equal(await page.locator('#dates button').first().getAttribute('data-day'), '2026-08-31');
      await checkAlignment();
    await page.screenshot({ path: `/tmp/opencode/calendar-${width}-previous.png`, fullPage: true });
    await page.locator('#next').click();
    await ready();
    assert.equal(await page.locator('#period-title').innerText(), weekCaption);
    await page.locator('[data-day="2026-09-09"]').click();
    assert.equal(await page.locator('.active-lesson').count(), 0);
    assert.deepEqual(await toolbar(), toolbarPositions);
    await page.reload();
    await ready();
    assert.equal(await page.locator('#dates button').count(), 7);
    assert.equal(await page.locator('#dates [aria-current=date]').getAttribute('aria-pressed'), 'true');
    assert.equal(await page.locator('#dates button').first().getAttribute('data-day'), '2026-09-07');
    assert.equal(await page.locator('.active-lesson').count(), 1);
    await checkAlignment();
    await page.screenshot({ path: `/tmp/opencode/calendar-${width}-active.png`, fullPage: true });
    current = null;
    await page.clock.runFor(60000);
    await ready();
    assert.equal(await page.locator('.active-lesson').count(), 0);
    assert.ok(!(await page.locator('body').innerText()).includes('Сейчас пары нет'));
    current = { ...lesson, ends_at: '2026-09-08T09:00:00Z' };
    await page.clock.runFor(60000);
    await ready();
    assert.equal(await page.locator('.active-lesson').count(), 1);
    current = null;
    await page.clock.runFor(60000);
    await ready();
    assert.equal(await page.locator('.active-lesson').count(), 0);
    for (const control of await page.locator('nav button').all()) {
      const box = await control.boundingBox();
      if (box) assert.ok(box.width >= 44 && box.height >= 44);
    }
    await page.screenshot({ path: `/tmp/opencode/calendar-${width}-week.png`, fullPage: true });
    await page.keyboard.press('Tab');
    await page.locator('#view [data-view=month]').focus();
    assert.equal(await page.locator('#view [data-view=month]').evaluate(el => getComputedStyle(el).outlineStyle), 'solid');
    await page.keyboard.press('Enter');
    await ready();
    assert.equal(await page.locator('#view [data-view=month]').getAttribute('aria-pressed'), 'true');
    assert.equal(await page.locator('#view [data-view=week]').getAttribute('aria-pressed'), 'false');
     assert.equal(await page.locator('#dates button').count(), 42);
      await checkAlignment();
     await page.screenshot({ path: `/tmp/opencode/calendar-${width}-month-hidden.png`, fullPage: true });
    await page.locator('#next').click();
    await ready();
     assert.equal(await page.locator('#dates button:not(:disabled)').first().getAttribute('data-day'), '2026-10-01');
      await checkAlignment();
     await page.screenshot({ path: `/tmp/opencode/calendar-${width}-month-visible.png`, fullPage: true });
    await page.locator('#previous').click();
    await ready();
    assert.equal(await page.locator('#dates button:not(:disabled)').first().getAttribute('data-day'), '2026-09-01');
    await page.locator('#next').click();
    await ready();
    await page.reload();
    await ready();
    assert.equal(await page.locator('#dates [aria-current=date]').getAttribute('aria-pressed'), 'true');
    const date = page.locator('#dates button:not(:disabled)').first();
    await date.click();
    assert.equal(await date.getAttribute('aria-pressed'), 'true');
    await page.keyboard.press('ArrowRight');
    assert.equal(await page.locator('#dates button:not(:disabled)').nth(1).evaluate(el => el === document.activeElement), true);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await page.screenshot({ path: `/tmp/opencode/calendar-${width}-month.png`, fullPage: true });
    await page.emulateMedia({ reducedMotion: 'reduce' });
    assert.equal(await date.evaluate(el => getComputedStyle(el).transitionDuration), '0s');
    const session = await page.context().newCDPSession(page);
    await session.send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-reduced-transparency', value: 'reduce' }] });
    assert.equal(await page.locator('.calendar').evaluate(el => getComputedStyle(el).backdropFilter), 'none');
    await session.send('Emulation.setEmulatedMedia', { features: [] });
    // Browser-only fixtures exercise long content without changing stored data.
    await page.route('**/api/public/v1/schedule?*', route => route.fulfill({ json: { events: ['lesson', 'event', 'exam'].map(category => ({
      title: `Проверка длинного названия: ${'Расписание'.repeat(8)}`,
      starts_at: new URL(route.request().url()).searchParams.get('from'),
      category, location: 'Аудитория 123 / учебный корпус',
    })) } }));
    await page.locator('#next').click();
    await ready();
    assert.equal(await page.locator('#agenda article').count(), 3);
    for (const category of ['lesson', 'event', 'exam']) assert.equal(await page.locator(`#agenda .category-${category}`).count(), 1);
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await page.screenshot({ path: `/tmp/opencode/calendar-${width}-agenda.png`, fullPage: true });
    await page.locator('h1').evaluate(el => { el.textContent = 'ОченьДлинноеНазваниеУчебнойГруппы'.repeat(4); });
    assert.ok(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth));
    await page.screenshot({ path: `/tmp/opencode/calendar-${width}-long-group.png`, fullPage: true });
    await page.evaluate(() => scrollTo(0, 150));
    const scroll = await page.evaluate(() => scrollY);
    await page.locator('#view [data-view=week]').evaluate(el => el.click());
    await ready();
    assert.equal(await page.evaluate(() => scrollY), scroll);
    assert.deepEqual(errors, []);
    await page.close();
     console.log(`${width}px: text ordering/spacing, caption, borderless pills, week/month navigation, reload reset, inline current event, keyboard, touch targets, overflow, reduced effects, scroll stability passed`);
  }
} finally {
  await browser.close();
}
