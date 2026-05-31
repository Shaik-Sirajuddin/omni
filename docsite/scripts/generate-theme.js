const fs = require('fs');
const path = require('path');

const themePath = path.join(__dirname, '../theme.json');
const outputPath = path.join(__dirname, '../src/css/material-theme.css');

if (!fs.existsSync(themePath)) {
  console.log('No theme.json found, skipping theme generation.');
  process.exit(0);
}

const themeData = JSON.parse(fs.readFileSync(themePath, 'utf8'));

let cssContent = `/* Auto-generated from theme.json */\n\n`;

// Generate light mode variables
cssContent += `:root {\n`;
if (themeData.schemes && themeData.schemes.light) {
  for (const [key, value] of Object.entries(themeData.schemes.light)) {
    cssContent += `  --md-sys-color-${key}: ${value};\n`;
  }
}
cssContent += `}\n\n`;

// Generate dark mode variables (Docusaurus uses [data-theme='dark'])
cssContent += `[data-theme='dark'] {\n`;
if (themeData.schemes && themeData.schemes.dark) {
  for (const [key, value] of Object.entries(themeData.schemes.dark)) {
    cssContent += `  --md-sys-color-${key}: ${value};\n`;
  }
}
cssContent += `}\n\n`;

// Map to Tailwind v4 @theme configuration
cssContent += `@theme {\n`;
if (themeData.schemes && themeData.schemes.light) {
  for (const key of Object.keys(themeData.schemes.light)) {
    // Defines utilities like text-primary, bg-secondary
    cssContent += `  --color-${key}: var(--md-sys-color-${key});\n`;
  }
}
cssContent += `}\n`;

fs.writeFileSync(outputPath, cssContent);
console.log('Material theme CSS generated successfully at src/css/material-theme.css');
