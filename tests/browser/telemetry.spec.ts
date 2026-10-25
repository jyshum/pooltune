import {test,expect} from '@playwright/test';
test('Tiger history persists across reload on both private dashboards',async({browser})=>{
 test.skip(!process.env.POOLTUNE_HISTORY_URL_FILE,'requires explicit Tiger Data connection');
 for(const [port,token,password] of [[12165,'m','private-managed-browser-password'],[12164,'t','private-browser-test-password']] as const){
  const context=await browser.newContext();const page=await context.newPage();await page.goto(`http://127.0.0.1:${port}`);
  await page.getByLabel('One-time setup token').fill(token.repeat(43));
  await page.getByLabel('Administrator password').fill(password);
  await page.getByRole('button',{name:/Claim installation|Sign in/,exact:true}).click();
  await page.getByRole('button',{name:'View history',exact:true}).click();
  const region=page.getByTestId('traffic-history');
  await expect(region.getByText('Simulated traffic',{exact:true}).first()).toBeVisible({timeout:25000});
  const before=await page.request.get(`http://127.0.0.1:${port}/api/telemetry`);const saved=await before.json();expect(saved.enabled).toBe(true);expect(saved.items.length).toBeGreaterThan(0);
  expect(saved.items.every((r:{demo:boolean})=>r.demo)).toBe(true);
  await page.reload();await page.getByRole('button',{name:'View history',exact:true}).click();
  await expect(page.getByTestId('traffic-history').getByText('Simulated traffic',{exact:true}).first()).toBeVisible();
  await page.screenshot({path:`test-results/history-${port}-desktop.png`,fullPage:true});
  await page.setViewportSize({width:390,height:844});expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
  await page.screenshot({path:`test-results/history-${port}-mobile.png`,fullPage:true});
  await context.close();
 }
});
