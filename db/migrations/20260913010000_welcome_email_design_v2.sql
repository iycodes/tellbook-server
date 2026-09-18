-- migrate:up
-- Generated from internal/welcomeemail/templates/welcome.html and design_test.go.
-- Drafts only: deploy URL-aware assignment code before activating either version.
-- Existing active templates and queued message snapshots are unchanged.

INSERT INTO welcome_email_templates (audience,version,name,subject_template,html_template,text_template,status)
VALUES ('provider',2,'Provider welcome — branded v2','Welcome to TellBook — make room for your best work',$welcome_html$<!doctype html>
<html lang="en" xmlns:o="urn:schemas-microsoft-com:office:office">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="x-apple-disable-message-reformatting"><meta name="color-scheme" content="light"><meta name="supported-color-schemes" content="light">
<title>Welcome to TellBook — make room for your best work</title>
<style>
:root{color-scheme:light only;supported-color-schemes:light}
body{margin:0;padding:0}table{border-collapse:collapse;mso-table-lspace:0pt;mso-table-rspace:0pt}
a[x-apple-data-detectors]{color:inherit!important;text-decoration:none!important}
@media screen and (max-width:480px){.outer{padding:20px 10px!important}.inset{padding-left:24px!important;padding-right:24px!important}.hero-title{font-size:37px!important;line-height:41px!important}}
</style>
<!--[if mso]><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml><![endif]-->
</head>
<body bgcolor="#faf9f4" style="margin:0;padding:0;width:100%;background-color:#faf9f4;color:#2d2426;font-family:Arial,Helvetica,sans-serif;-webkit-text-size-adjust:100%;-ms-text-size-adjust:100%;">
<div aria-hidden="true" style="display:none;font-size:1px;line-height:1px;color:#faf9f4;max-height:0;max-width:0;opacity:0;overflow:hidden;mso-hide:all;">Your provider account is ready. Let’s make it yours.&#847; &zwnj; &nbsp; &zwnj; &nbsp; &zwnj; &nbsp; &zwnj; &nbsp; &zwnj; &nbsp; &zwnj; &nbsp;</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#faf9f4" style="width:100%;background-color:#faf9f4;">
<tr><td class="outer" align="center" style="padding:36px 16px;">
<!--[if mso]><table role="presentation" align="center" width="600" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="width:100%;max-width:600px;table-layout:fixed;">
        <tr><td style="padding:0 4px 22px;">
          <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">
            <tr>
              <td valign="middle">
                <table role="presentation" cellpadding="0" cellspacing="0" border="0">
                  <tr>
                    <td width="40" valign="middle" style="width:40px;"><img src="https://bookly-public.iycodes.com/bookings.png" width="40" height="40" alt="" style="display:block;width:40px;height:40px;border:0;outline:none;text-decoration:none;"></td>
                    <td valign="middle" style="padding-left:8px;font-family:Arial,Helvetica,sans-serif;font-size:24px;line-height:30px;font-weight:700;letter-spacing:-0.8px;white-space:nowrap;"><span style="color:#af1f4a;">Tell</span><span style="color:#2d2426;">Book</span></td>
                  </tr>
                </table>
              </td>
              <td align="right" style="font-family:Arial,Helvetica,sans-serif;font-size:11px;line-height:16px;letter-spacing:1.5px;color:#584144;">A LITTLE MORE<br>TAKEN CARE OF.</td>
            </tr>
          </table>
        </td></tr>

<tr><td style="border:1px solid #e2d8d2;border-top:4px solid #af1f4a;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#ffffff" style="width:100%;background-color:#ffffff;table-layout:fixed;">
<tr><td class="inset" bgcolor="#f7e9ee" style="padding:32px 40px 0;background-color:#f7e9ee;font-family:Arial,Helvetica,sans-serif;font-size:10px;line-height:16px;letter-spacing:2px;font-weight:700;color:#af1f4a;">FOR THE WORK YOU LOVE</td></tr>
<tr><td class="inset" bgcolor="#f7e9ee" style="padding:20px 40px 0;background-color:#f7e9ee;">
<h1 class="hero-title" style="margin:0;font-family:Georgia,'Times New Roman',serif;font-size:47px;line-height:51px;font-weight:400;letter-spacing:-1.2px;color:#2d2426;">More time for<br><span style="color:#af1f4a;">your best work.</span></h1>
</td></tr>
<tr><td class="inset" bgcolor="#f7e9ee" style="padding:24px 40px 30px;background-color:#f7e9ee;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr><td style="width:28px;border-top:2px solid #af1f4a;font-size:1px;line-height:1px;" width="28">&nbsp;</td><td style="padding-left:12px;font-family:Arial,Helvetica,sans-serif;font-size:11px;line-height:17px;color:#584144;">Your provider account is ready</td></tr></table>
</td></tr>
<tr><td class="inset" style="padding:30px 40px 22px;font-family:Arial,Helvetica,sans-serif;font-size:15px;line-height:24px;color:#584144;overflow-wrap:break-word;word-wrap:break-word;">
<p style="margin:0 0 10px;color:#2d2426;">Hi {{name}},</p><p style="margin:0;">Welcome to TellBook. You bring the skill; we’ll help you keep the bookings, payments, and conversations in one place.</p>
</td></tr>
<tr><td class="inset" style="padding:0 40px 30px;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr><td align="center" bgcolor="#af1f4a" style="background-color:#af1f4a;border:1px solid #af1f4a;mso-padding-alt:15px 20px;"><a href="{{action_url}}" style="display:inline-block;padding:15px 20px;border:1px solid #af1f4a;font-family:Arial,Helvetica,sans-serif;font-size:14px;line-height:20px;font-weight:700;color:#ffffff;text-decoration:none;text-align:center;mso-padding-alt:0;">Open my workspace &nbsp; &rarr;</a></td></tr></table>
</td></tr>
<tr><td class="inset" style="padding:0 40px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="width:100%;table-layout:fixed;">
<tr><td style="padding:24px 0 6px;border-top:1px solid #e2d8d2;font-family:Arial,Helvetica,sans-serif;font-size:10px;line-height:16px;letter-spacing:1.7px;font-weight:700;color:#7c6a6e;">A GOOD PLACE TO BEGIN</td></tr>
<tr><td style="padding:0 0 18px;"><h2 style="margin:0;font-family:Georgia,'Times New Roman',serif;font-size:25px;line-height:32px;font-weight:400;color:#2d2426;">Make yourself at home.</h2></td></tr>

<tr><td style="padding:0 0 20px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="width:100%;table-layout:fixed;"><tr>
<td valign="top" width="42" style="width:42px;padding:0 8px 0 0;font-family:Georgia,'Times New Roman',serif;font-size:25px;line-height:28px;color:#af1f4a;">01</td>
<td valign="top" style="overflow-wrap:break-word;word-wrap:break-word;"><h3 style="margin:0 0 4px;font-family:Arial,Helvetica,sans-serif;font-size:14px;line-height:22px;font-weight:700;color:#2d2426;">Make it yours</h3><p style="margin:0;font-family:Arial,Helvetica,sans-serif;font-size:13px;line-height:21px;color:#584144;">Add your business details, location, and availability.</p></td>
</tr></table>
</td></tr>

<tr><td style="padding:0 0 20px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="width:100%;table-layout:fixed;"><tr>
<td valign="top" width="42" style="width:42px;padding:0 8px 0 0;font-family:Georgia,'Times New Roman',serif;font-size:25px;line-height:28px;color:#af1f4a;">02</td>
<td valign="top" style="overflow-wrap:break-word;word-wrap:break-word;"><h3 style="margin:0 0 4px;font-family:Arial,Helvetica,sans-serif;font-size:14px;line-height:22px;font-weight:700;color:#2d2426;">Create your first service</h3><p style="margin:0;font-family:Arial,Helvetica,sans-serif;font-size:13px;line-height:21px;color:#584144;">Set your pricing and explain what customers can expect.</p></td>
</tr></table>
</td></tr>

<tr><td style="padding:0 0 20px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="width:100%;table-layout:fixed;"><tr>
<td valign="top" width="42" style="width:42px;padding:0 8px 0 0;font-family:Georgia,'Times New Roman',serif;font-size:25px;line-height:28px;color:#af1f4a;">03</td>
<td valign="top" style="overflow-wrap:break-word;word-wrap:break-word;"><h3 style="margin:0 0 4px;font-family:Arial,Helvetica,sans-serif;font-size:14px;line-height:22px;font-weight:700;color:#2d2426;">Share your booking page</h3><p style="margin:0;font-family:Arial,Helvetica,sans-serif;font-size:13px;line-height:21px;color:#584144;">Give customers one place to choose a service and book with you.</p></td>
</tr></table>
</td></tr>

</table>
</td></tr>
<tr><td class="inset" style="padding:4px 40px 28px;font-family:Arial,Helvetica,sans-serif;font-size:13px;line-height:22px;color:#584144;"><p style="margin:0 0 14px;">Start with one service. You can build the rest as you go.</p><p style="margin:0;color:#2d2426;">Glad you’re here,<br><strong>The TellBook team</strong></p></td></tr>
<tr><td class="inset" bgcolor="#faf9f4" style="padding:18px 40px;background-color:#faf9f4;border-top:1px solid #e2d8d2;font-family:Arial,Helvetica,sans-serif;font-size:12px;line-height:19px;color:#584144;">Your workspace is ready for you to set up. Your services become bookable when you publish them.</td></tr>
</table>
</td></tr>
<tr><td align="center" style="padding:22px 20px 0;font-family:Arial,Helvetica,sans-serif;font-size:12px;line-height:19px;color:#7c6a6e;"><p style="margin:0 0 5px;font-weight:700;color:#584144;">Your time. Well booked.</p><p style="margin:0;">You’re receiving this because you created a TellBook account.<br><span style="word-break:break-all;overflow-wrap:break-word;">{{email}}</span></p><p style="margin:10px 0 0;"><a href="{{action_url}}" style="color:#af1f4a;text-decoration:underline;">Go to my workspace</a></p></td></tr>
</table>
<!--[if mso]></td></tr></table><![endif]-->
</td></tr></table>
</body></html>
$welcome_html$,$welcome_text$Hi {{name}},

Welcome to TellBook. You bring the skill; we’ll help you keep the bookings, payments, and conversations in one place.

Open my workspace: {{action_url}}

Make yourself at home.

01. Make it yours
Add your business details, location, and availability.

02. Create your first service
Set your pricing and explain what customers can expect.

03. Share your booking page
Give customers one place to choose a service and book with you.

Start with one service. You can build the rest as you go.

Glad you’re here,
The TellBook team

Your workspace is ready for you to set up. Your services become bookable when you publish them.

Account: {{email}}
$welcome_text$,'draft')
ON CONFLICT (audience,version) DO NOTHING;

INSERT INTO welcome_email_templates (audience,version,name,subject_template,html_template,text_template,status)
VALUES ('marketplace_customer',2,'Customer welcome — branded v2','Welcome to TellBook — good plans start here',$welcome_html$<!doctype html>
<html lang="en" xmlns:o="urn:schemas-microsoft-com:office:office">
<head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="x-apple-disable-message-reformatting"><meta name="color-scheme" content="light"><meta name="supported-color-schemes" content="light">
<title>Welcome to TellBook — good plans start here</title>
<style>
:root{color-scheme:light only;supported-color-schemes:light}
body{margin:0;padding:0}table{border-collapse:collapse;mso-table-lspace:0pt;mso-table-rspace:0pt}
a[x-apple-data-detectors]{color:inherit!important;text-decoration:none!important}
@media screen and (max-width:480px){.outer{padding:20px 10px!important}.inset{padding-left:24px!important;padding-right:24px!important}.hero-title{font-size:37px!important;line-height:41px!important}}
</style>
<!--[if mso]><xml><o:OfficeDocumentSettings><o:PixelsPerInch>96</o:PixelsPerInch></o:OfficeDocumentSettings></xml><![endif]-->
</head>
<body bgcolor="#faf9f4" style="margin:0;padding:0;width:100%;background-color:#faf9f4;color:#2d2426;font-family:Arial,Helvetica,sans-serif;-webkit-text-size-adjust:100%;-ms-text-size-adjust:100%;">
<div aria-hidden="true" style="display:none;font-size:1px;line-height:1px;color:#faf9f4;max-height:0;max-width:0;opacity:0;overflow:hidden;mso-hide:all;">Find your next service, choose your time, and keep your plans together.&#847; &zwnj; &nbsp; &zwnj; &nbsp; &zwnj; &nbsp; &zwnj; &nbsp; &zwnj; &nbsp; &zwnj; &nbsp;</div>
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#faf9f4" style="width:100%;background-color:#faf9f4;">
<tr><td class="outer" align="center" style="padding:36px 16px;">
<!--[if mso]><table role="presentation" align="center" width="600" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="width:100%;max-width:600px;table-layout:fixed;">
        <tr><td style="padding:0 4px 22px;">
          <table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">
            <tr>
              <td valign="middle">
                <table role="presentation" cellpadding="0" cellspacing="0" border="0">
                  <tr>
                    <td width="40" valign="middle" style="width:40px;"><img src="https://bookly-public.iycodes.com/bookings.png" width="40" height="40" alt="" style="display:block;width:40px;height:40px;border:0;outline:none;text-decoration:none;"></td>
                    <td valign="middle" style="padding-left:8px;font-family:Arial,Helvetica,sans-serif;font-size:24px;line-height:30px;font-weight:700;letter-spacing:-0.8px;white-space:nowrap;"><span style="color:#af1f4a;">Tell</span><span style="color:#2d2426;">Book</span></td>
                  </tr>
                </table>
              </td>
              <td align="right" style="font-family:Arial,Helvetica,sans-serif;font-size:11px;line-height:16px;letter-spacing:1.5px;color:#584144;">A LITTLE MORE<br>TAKEN CARE OF.</td>
            </tr>
          </table>
        </td></tr>

<tr><td style="border:1px solid #e2d8d2;border-top:4px solid #af1f4a;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" bgcolor="#ffffff" style="width:100%;background-color:#ffffff;table-layout:fixed;">
<tr><td class="inset" bgcolor="#f7e9ee" style="padding:32px 40px 0;background-color:#f7e9ee;font-family:Arial,Helvetica,sans-serif;font-size:10px;line-height:16px;letter-spacing:2px;font-weight:700;color:#af1f4a;">FOR YOUR NEXT GOOD PLAN</td></tr>
<tr><td class="inset" bgcolor="#f7e9ee" style="padding:20px 40px 0;background-color:#f7e9ee;">
<h1 class="hero-title" style="margin:0;font-family:Georgia,'Times New Roman',serif;font-size:47px;line-height:51px;font-weight:400;letter-spacing:-1.2px;color:#2d2426;">Good plans<br><span style="color:#af1f4a;">start here.</span></h1>
</td></tr>
<tr><td class="inset" bgcolor="#f7e9ee" style="padding:24px 40px 30px;background-color:#f7e9ee;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr><td style="width:28px;border-top:2px solid #af1f4a;font-size:1px;line-height:1px;" width="28">&nbsp;</td><td style="padding-left:12px;font-family:Arial,Helvetica,sans-serif;font-size:11px;line-height:17px;color:#584144;">Your customer account is ready</td></tr></table>
</td></tr>
<tr><td class="inset" style="padding:30px 40px 22px;font-family:Arial,Helvetica,sans-serif;font-size:15px;line-height:24px;color:#584144;overflow-wrap:break-word;word-wrap:break-word;">
<p style="margin:0 0 10px;color:#2d2426;">Hi {{name}},</p><p style="margin:0;">Find a service you’ll love, book a time that works, and keep the details close. Welcome to TellBook.</p>
</td></tr>
<tr><td class="inset" style="padding:0 40px 30px;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr><td align="center" bgcolor="#af1f4a" style="background-color:#af1f4a;border:1px solid #af1f4a;mso-padding-alt:15px 20px;"><a href="{{action_url}}" style="display:inline-block;padding:15px 20px;border:1px solid #af1f4a;font-family:Arial,Helvetica,sans-serif;font-size:14px;line-height:20px;font-weight:700;color:#ffffff;text-decoration:none;text-align:center;mso-padding-alt:0;">Explore services &nbsp; &rarr;</a></td></tr></table>
</td></tr>
<tr><td class="inset" style="padding:0 40px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="width:100%;table-layout:fixed;">
<tr><td style="padding:24px 0 6px;border-top:1px solid #e2d8d2;font-family:Arial,Helvetica,sans-serif;font-size:10px;line-height:16px;letter-spacing:1.7px;font-weight:700;color:#7c6a6e;">A GOOD PLACE TO BEGIN</td></tr>
<tr><td style="padding:0 0 18px;"><h2 style="margin:0;font-family:Georgia,'Times New Roman',serif;font-size:25px;line-height:32px;font-weight:400;color:#2d2426;">Your next booking, made simpler.</h2></td></tr>

<tr><td style="padding:0 0 20px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="width:100%;table-layout:fixed;"><tr>
<td valign="top" width="42" style="width:42px;padding:0 8px 0 0;font-family:Georgia,'Times New Roman',serif;font-size:25px;line-height:28px;color:#af1f4a;">01</td>
<td valign="top" style="overflow-wrap:break-word;word-wrap:break-word;"><h3 style="margin:0 0 4px;font-family:Arial,Helvetica,sans-serif;font-size:14px;line-height:22px;font-weight:700;color:#2d2426;">Find your kind of service</h3><p style="margin:0;font-family:Arial,Helvetica,sans-serif;font-size:13px;line-height:21px;color:#584144;">Explore providers, compare services, and pick what fits.</p></td>
</tr></table>
</td></tr>

<tr><td style="padding:0 0 20px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="width:100%;table-layout:fixed;"><tr>
<td valign="top" width="42" style="width:42px;padding:0 8px 0 0;font-family:Georgia,'Times New Roman',serif;font-size:25px;line-height:28px;color:#af1f4a;">02</td>
<td valign="top" style="overflow-wrap:break-word;word-wrap:break-word;"><h3 style="margin:0 0 4px;font-family:Arial,Helvetica,sans-serif;font-size:14px;line-height:22px;font-weight:700;color:#2d2426;">Choose a time that works</h3><p style="margin:0;font-family:Arial,Helvetica,sans-serif;font-size:13px;line-height:21px;color:#584144;">Review the details and complete the booking steps.</p></td>
</tr></table>
</td></tr>

<tr><td style="padding:0 0 20px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="width:100%;table-layout:fixed;"><tr>
<td valign="top" width="42" style="width:42px;padding:0 8px 0 0;font-family:Georgia,'Times New Roman',serif;font-size:25px;line-height:28px;color:#af1f4a;">03</td>
<td valign="top" style="overflow-wrap:break-word;word-wrap:break-word;"><h3 style="margin:0 0 4px;font-family:Arial,Helvetica,sans-serif;font-size:14px;line-height:22px;font-weight:700;color:#2d2426;">Keep your plans together</h3><p style="margin:0;font-family:Arial,Helvetica,sans-serif;font-size:13px;line-height:21px;color:#584144;">Your bookings, saved favourites, and conversations stay in your account.</p></td>
</tr></table>
</td></tr>

</table>
</td></tr>
<tr><td class="inset" style="padding:4px 40px 28px;font-family:Arial,Helvetica,sans-serif;font-size:13px;line-height:22px;color:#584144;"><p style="margin:0 0 14px;">Something for your everyday. Something for a special day. We’re glad to be part of your plans.</p><p style="margin:0;color:#2d2426;">Glad you’re here,<br><strong>The TellBook team</strong></p></td></tr>
<tr><td class="inset" bgcolor="#faf9f4" style="padding:18px 40px;background-color:#faf9f4;border-top:1px solid #e2d8d2;font-family:Arial,Helvetica,sans-serif;font-size:12px;line-height:19px;color:#584144;">This is your account welcome. You’ll get a separate update when you make a booking.</td></tr>
</table>
</td></tr>
<tr><td align="center" style="padding:22px 20px 0;font-family:Arial,Helvetica,sans-serif;font-size:12px;line-height:19px;color:#7c6a6e;"><p style="margin:0 0 5px;font-weight:700;color:#584144;">Your time. Well booked.</p><p style="margin:0;">You’re receiving this because you created a TellBook account.<br><span style="word-break:break-all;overflow-wrap:break-word;">{{email}}</span></p><p style="margin:10px 0 0;"><a href="{{action_url}}" style="color:#af1f4a;text-decoration:underline;">Find my next service</a></p></td></tr>
</table>
<!--[if mso]></td></tr></table><![endif]-->
</td></tr></table>
</body></html>
$welcome_html$,$welcome_text$Hi {{name}},

Find a service you’ll love, book a time that works, and keep the details close. Welcome to TellBook.

Explore services: {{action_url}}

Your next booking, made simpler.

01. Find your kind of service
Explore providers, compare services, and pick what fits.

02. Choose a time that works
Review the details and complete the booking steps.

03. Keep your plans together
Your bookings, saved favourites, and conversations stay in your account.

Something for your everyday. Something for a special day. We’re glad to be part of your plans.

Glad you’re here,
The TellBook team

This is your account welcome. You’ll get a separate update when you make a booking.

Account: {{email}}
$welcome_text$,'draft')
ON CONFLICT (audience,version) DO NOTHING;

-- migrate:down
-- Keep version rows because immutable sent/queued jobs may reference them.
-- Restore a previous active version explicitly if rolling back an activation.
UPDATE welcome_email_templates SET status='archived',updated_at=NOW()
WHERE version=2 AND name IN ('Provider welcome — branded v2','Customer welcome — branded v2');
