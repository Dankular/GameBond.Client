// Captive hotspot settings and voucher management.
const hotspotDialog = modal('hotspotdialog', 'Captive hotspot portal');
const hotspotFields = element('div'); hotspotFields.className = 'fields';
const hotspotMessage = element('p', '', { role: 'status' });
const guestChecks = element('div'); guestChecks.className = 'accesslist';
const hotspotValues = {};
const hotspotEnabled = input('checkbox', '', 'Enable captive hotspot');
hotspotFields.append(field('Enabled', hotspotEnabled, hotspotFields), element('span', ''));
function hotspotField(name, label, type = 'text') {
  const control = input(type, '', label);
  if (type === 'number') control.min = '0';
  hotspotValues[name] = field(label, control, hotspotFields);
}
for (const [name, label, type] of [
  ['listen', 'HTTPS listen address'], ['hostname', 'Portal hostname'], ['tls_cert', 'TLS certificate path'], ['tls_key', 'TLS private key path'],
  ['brand_name', 'Landing page name'], ['logo_file', 'Local PNG, JPEG or WebP logo path'], ['primary_color', 'Brand color (#RRGGBB)'], ['terms_url', 'Terms URL'], ['session_seconds', 'Session duration (seconds)', 'number'],
  ['password', 'Set/change shared password (blank keeps current)', 'password'], ['external_url', 'External portal HTTPS URL'], ['external_secret', 'External secret or file:/path (write only)', 'password'],
  ['payment_endpoint', 'Payment adapter HTTPS URL'], ['payment_secret', 'Payment secret or file:/path (write only)', 'password'], ['payment_webhook_secret', 'Webhook secret or file:/path (write only)', 'password'],
  ['payment_plan', 'Payment plan ID'], ['payment_currency', 'Payment currency'], ['payment_amount_minor', 'Payment amount in minor units', 'number']
]) hotspotField(name, label, type || 'text');
hotspotDialog.append(element('p', 'Select only guest adapters. Changes require a client restart.'), hotspotFields, element('h3', 'Guest devices'), guestChecks);

const saveHotspot = action('Save hotspot settings', async () => {
  try {
    hotspotMessage.textContent = 'Saving…';
    const body = { enabled: hotspotEnabled.checked, devices: Array.from(guestChecks.querySelectorAll('input:checked')).map(c => c.value) };
    for (const key of ['listen','hostname','tls_cert','tls_key','brand_name','logo_file','primary_color','terms_url','password','external_url','external_secret','payment_endpoint','payment_secret','payment_webhook_secret','payment_plan','payment_currency']) body[key] = hotspotValues[key].value.trim();
    body.session_seconds = Number(hotspotValues.session_seconds.value || 0);
    body.payment_amount_minor = Number(hotspotValues.payment_amount_minor.value || 0);
    await api('hotspot', 'PUT', body);
    hotspotMessage.textContent = 'Saved. Restart the client to apply settings.';
  } catch (e) { hotspotMessage.textContent = e.message; }
});
hotspotDialog.append(saveHotspot, hotspotMessage);

const voucherBox = element('fieldset'), voucherLegend = element('legend', 'Vouchers'), voucherControls = element('div');
voucherControls.className = 'row';
const voucherUses = input('number', '1', 'Voucher uses', { min: 1, max: 100000 });
const voucherDuration = input('number', '0', 'Voucher duration seconds (0 uses default)', { min: 0 });
const voucherExpiry = input('datetime-local', '', 'Voucher expiry');
const voucherResult = element('p', '', { role: 'status' });
voucherControls.append(field('Uses', voucherUses, voucherControls), field('Duration (seconds)', voucherDuration, voucherControls), field('Expires at', voucherExpiry, voucherControls));
const createVoucher = action('Create voucher', async () => {
  try {
    const expires = voucherExpiry.value ? new Date(voucherExpiry.value).toISOString() : '';
    const result = await api('hotspot/vouchers', 'POST', { uses: Number(voucherUses.value), duration_seconds: Number(voucherDuration.value), expires_at: expires });
    voucherResult.textContent = 'New voucher (shown once): ' + result.voucher + ' · Save it before restarting.';
  } catch (e) { voucherResult.textContent = e.message; }
});
const revokeInput = input('password', '', 'Voucher code to revoke');
const revokeVoucher = action('Revoke voucher', async () => {
  try { await api('hotspot/vouchers', 'DELETE', { code: revokeInput.value }); voucherResult.textContent = 'Voucher removed. Restart the client to apply.'; revokeInput.value = ''; }
  catch (e) { voucherResult.textContent = e.message; }
});
voucherBox.append(voucherLegend, element('p', 'Voucher codes are shown only when created. Existing voucher hashes are never shown.'), voucherControls, createVoucher, revokeInput, revokeVoucher, voucherResult);
hotspotDialog.append(voucherBox);
const guestSessionsBox = element('fieldset'), guestSessionsLegend = element('legend', 'Active guest sessions'), guestSessionsList = element('div');
guestSessionsBox.append(guestSessionsLegend, guestSessionsList); hotspotDialog.append(guestSessionsBox);

async function openHotspot() {
  try {
    const result = await api('hotspot'), config = result.hotspot || {}, lan = await api('lan-devices'), runtime = await api('status');
    hotspotEnabled.checked = !!config.enabled;
    guestChecks.replaceChildren();
    for (const device of lan.lan_devices || []) {
      const check = input('checkbox', '', 'Guest device ' + device.name);
      check.value = device.name; check.checked = (config.devices || []).includes(device.name);
      guestChecks.append(field(device.name, check, guestChecks));
    }
    for (const key of ['listen','hostname','tls_cert','tls_key','brand_name','logo_file','primary_color','terms_url','session_seconds','external_url','payment_endpoint','payment_plan','payment_currency','payment_amount_minor']) {
      hotspotValues[key].value = config[key] ?? (key === 'session_seconds' ? 28800 : '');
    }
    for (const key of ['password','external_secret','payment_secret','payment_webhook_secret']) hotspotValues[key].value = '';
    hotspotValues._voucherCount = Number(config.voucher_count || 0);
    hotspotMessage.textContent = (config.has_password ? 'Password configured. ' : 'No password configured. ') + (config.external_configured ? 'External portal secret configured. ' : '') + (config.payment_configured ? 'Payment secret configured.' : '');
    voucherResult.textContent = 'Configured vouchers: ' + hotspotValues._voucherCount + '. Restart after changes.';
    guestSessionsList.replaceChildren();
    const sessions = runtime.hotspot?.sessions || [];
    if (!sessions.length) guestSessionsList.append(element('p', 'No active sessions.'));
    for (const session of sessions) {
      const row = element('div'); row.className = 'row';
      row.append(element('span', session.guest + ' · expires ' + new Date(Number(session.expires_at) * 1000).toLocaleString()));
      row.append(action('Revoke access', async () => { try { await api('hotspot/sessions', 'POST', { guest: session.guest }); row.firstChild.textContent = session.guest + ' · revocation queued'; } catch (e) { row.firstChild.textContent = e.message; } }));
      guestSessionsList.append(row);
    }
    showDialog('hotspotdialog');
  } catch (e) { $('actionmsg').textContent = e.message; }
}
settingsMenu.append(action('Captive hotspot portal', () => { settingsDialog.close(); openHotspot(); }));
