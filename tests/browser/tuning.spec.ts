import {test,expect} from '@playwright/test';

test('capacity dashboard uses private owner API and labels provider gaps',async({page})=>{
 await page.goto('http://127.0.0.1:12164');
 await page.getByLabel('One-time setup token').fill('t'.repeat(43));
 await page.getByLabel('Administrator password').fill('private-browser-test-password');
 await page.getByRole('button',{name:'Claim installation'}).click();
 await expect(page.getByRole('heading',{name:'Pool capacity'})).toBeVisible();
 await expect(page.getByText('Connection ceiling',{exact:true})).toBeVisible();
 await expect(page.getByRole('region',{name:'Transaction throughput'})).toBeVisible();
 await expect(page.getByText('Simulated traffic',{exact:true})).toBeVisible();
 await expect(page.getByTestId('transaction-rate')).not.toHaveText('Unavailable',{timeout:6000});
 await expect(page.getByRole('button',{name:'Resume automation'})).toBeEnabled();
 await page.getByRole('button',{name:'Resume automation'}).click();
 await expect(page.getByRole('button',{name:'Freeze automation'})).toBeVisible();
 await page.getByRole('button',{name:'Freeze automation'}).click();
 await expect(page.getByRole('button',{name:'Resume automation'})).toBeVisible();
 await expect(page.getByText('Cloud automation unverified',{exact:true})).toBeVisible();
 await expect(page.getByRole('button',{name:'Ask AI for a draft'})).toHaveCount(0);
 await page.getByRole('button',{name:'History',exact:true}).click();
 await expect(page.getByRole('cell',{name:'Owner froze elective automation'})).toBeVisible();
 await page.screenshot({path:'test-results/tuning-history.png',fullPage:true});
 await page.getByRole('button',{name:'Overview',exact:true}).click();
 await page.setViewportSize({width:390,height:844});
 await expect(page.getByRole('heading',{name:'Pool capacity'})).toBeVisible();
 expect(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth)).toBe(true);
 await page.screenshot({path:'test-results/tuning-mobile.png',fullPage:true});
 // Cached observations must stop looking live when polling loses the backend.
 await page.route('**/api/state',()=>{});
 await expect(page.getByText('Observation required',{exact:true})).toBeVisible({timeout:8000});
 await expect(page.getByRole('button',{name:'Resume automation'})).toBeDisabled();
 await expect(page.getByTestId('transaction-rate')).toHaveText('Unavailable');
 await expect(page.getByTestId('traffic-status')).toHaveText('Data unavailable');
 await expect(page.getByRole('alert')).toContainText('timed out',{timeout:8000});
});
