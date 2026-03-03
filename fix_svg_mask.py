import re

def fix_mask(filepath):
    with open(filepath, 'r') as f:
        content = f.read()
    
    # Let's fix the invalid SVG structure. My previous regex might have messed up the tags.
    # The error is likely the <svg  defs> or similar malformed tag.
    
    # Re-read raw original file and apply mask properly
    base_file = filepath.replace("-apple", "")
    with open(base_file, 'r') as f:
        base_content = f.read()

    squircle_path = """<clipPath id="squircle">
        <path d="M512,0 C136.5,0 0,136.5 0,512 C0,887.5 136.5,1024 512,1024 C887.5,1024 1024,887.5 1024,512 C1024,136.5 887.5,0 512,0 Z" />
    </clipPath>"""

    # We will wrap everything inside the <svg> element in a <g clip-path="url(#squircle)">
    # First, let's find the first <defs> block to insert our clipPath
    if '<defs>' in base_content:
        fixed_content = base_content.replace('<defs>', f'<defs>\n{squircle_path}\n', 1)
    else:
        # Insert defs right after the opening svg tag
        fixed_content = re.sub(r'(<svg[^>]*>)', r'\1\n<defs>\n' + squircle_path + '\n</defs>\n', base_content, count=1)
        
    # Now, we need to extract everything BETWEEN the opening <svg> tag and the closing </svg> tag
    # and wrap it in the <g> tag, except we should probably leave <defs> alone.
    # Actually, a simpler way is to just apply the clip path directly to the paths that determine the background which are the first elements.
    # BUT, to clip EVERYTHING, we can just replace the closing </svg> to include the closing </g>,
    # and add <g clip-path="url(#squircle)"> right after the opening tags.
    
    # Find the end of the <svg> opening tag
    match = re.search(r'<svg[^>]*>', fixed_content)
    if match:
        svg_open = match.group(0)
        # Split into the SVG tag and the rest
        rest = fixed_content[len(svg_open):]
        # Remove the closing tag temporarily
        rest = rest.replace('</svg>', '')
        
        # Put it all together
        final_content = f"{svg_open}\n<g clip-path=\"url(#squircle)\">{rest}</g>\n</svg>"
        
        with open(filepath, 'w') as f:
            f.write(final_content)

fix_mask("/Volumes/MacExt/Sites/OpsView/icon-dark-apple.svg")
fix_mask("/Volumes/MacExt/Sites/OpsView/icon-light-apple.svg")
print("Fixed masking!")
