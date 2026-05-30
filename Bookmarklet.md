# Bookmarklet

```javascript
javascript:(function(){var s='https://script.google.com/macros/s/XXXXXXXX/exec?url=%27+encodeURIComponent(location.origin+location.pathname)+%27&title=%27+encodeURIComponent(document.title);new%20Image().src=s;var%20d=document.createElement(%27div%27);d.textContent=%27%E2%9C%93%20Saved%20to%20Sheets%27;d.style.cssText=%27position:fixed;top:20px;right:20px;background:#0f9d58;color:white;padding:12px%2020px;border-radius:8px;font-size:16px;z-index:999999;box-shadow:0%202px%208px%20rgba(0,0,0,0.3)';document.body.appendChild(d);setTimeout(function(){d.remove()},3000);})();
```



# Google App Script function (on Google Spreadsheet)

```javascript
function doGet(e) {
  // existing behaviour: bookmarklet sends ?url= to append a row
  if (e.parameter.url) {
    var sheet = SpreadsheetApp.getActiveSpreadsheet().getSheets()[0];
    sheet.appendRow([e.parameter.url]);
    return ContentService.createTextOutput('ok');
  }
  // new: ?action=list returns the URL column, one per line
  if (e.parameter.action === 'list') {
    var sheet = SpreadsheetApp.getActiveSpreadsheet().getSheets()[0];
    var values = sheet.getRange('A1:A').getValues();
    var urls = values.map(function(r){ return String(r[0]).trim(); })
                      .filter(function(u){ return u.indexOf('http') === 0; });
    return ContentService.createTextOutput(urls.join('\n'))
                          .setMimeType(ContentService.MimeType.PLAIN_TEXT);
  }
  return ContentService.createTextOutput('');
}
```

