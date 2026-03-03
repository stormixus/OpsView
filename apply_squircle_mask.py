import re

def apply_mask(filepath, out_filepath):
    with open(filepath, 'r') as f:
        content = f.read()
    
    # Apple squircle path for 1024x1024
    # Standard super-ellipse approximation for 1024
    squircle_path = """
    <clipPath id="squircle">
        <path d="M512,0 C136.5,0 0,136.5 0,512 C0,887.5 136.5,1024 512,1024 C887.5,1024 1024,887.5 1024,512 C1024,136.5 887.5,0 512,0 Z" />
    </clipPath>
    """
    
    # Insert clip path into defs
    if '<defs>' in content:
        content = content.replace('<defs>', f'<defs>\n{squircle_path}', 1)
    else:
        content = content.replace('<svg', f'<svg\n  defs>\n{squircle_path}</defs>\n', 1)

    # Wrap the entire content in a generic g tag using the clip path
    content = content.replace('</defs>', '</defs>\n  <g clip-path="url(#squircle)">')
    content = content.replace('</svg>', '  </g>\n</svg>')

    with open(out_filepath, 'w') as f:
        f.write(content)

apply_mask("/Volumes/MacExt/Sites/OpsView/icon-dark.svg", "/Volumes/MacExt/Sites/OpsView/icon-dark-apple.svg")
apply_mask("/Volumes/MacExt/Sites/OpsView/icon-light.svg", "/Volumes/MacExt/Sites/OpsView/icon-light-apple.svg")
print("Done formatting SVGs for macOS!")
