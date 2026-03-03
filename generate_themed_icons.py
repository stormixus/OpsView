import re

svg_path = "/Volumes/MacExt/Sites/OpsView/a-premium-desktop-application-icon-for-a-remote-mo.svg"
with open(svg_path, 'r') as f:
    content = f.read()

# For Dark Mode: Make it deeper and more neon
# Replace background gradient to be pure black to very dark blue
dark_content = re.sub(r'<stop offset="0" stop-color="[^"]*"/>', r'<stop offset="0" stop-color="#000000"/>', content, count=1)
dark_content = re.sub(r'<stop offset="1" stop-color="#002041"/>', r'<stop offset="1" stop-color="#000a14"/>', dark_content)

# For Light Mode: Brighten the background and adjust icon tones
# Replace background gradient to be pure white to soft silver/blue
light_content = re.sub(r'<stop offset="0" stop-color="[^"]*"/>', r'<stop offset="0" stop-color="#ffffff"/>', content, count=1)
light_content = re.sub(r'<stop offset="1" stop-color="#002041"/>', r'<stop offset="1" stop-color="#e0e8f5"/>', light_content)

# Optional: You could do hex color shifting here for all fills, but background is the most striking difference.
# Let's write them out.
with open("/Volumes/MacExt/Sites/OpsView/icon-dark.svg", 'w') as f:
    f.write(dark_content)

with open("/Volumes/MacExt/Sites/OpsView/icon-light.svg", 'w') as f:
    f.write(light_content)

print("Generated icon-dark.svg and icon-light.svg")
