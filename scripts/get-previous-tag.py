import os
import re
from git import Repo
from packaging.version import Version, InvalidVersion

def get_closest_lower_tag():
    current_raw = os.getenv("CURRENT_VERSION")
    if not current_raw:
        print("Error: environment variable 'CURRENT_VERSION' not set.")
        return

    try:
        current_v = Version(current_raw)
    except InvalidVersion:
        print(f"Error: '{current_raw}' is not a valid SemVer string.")
        return

    try:
        repo = Repo("..")
    except Exception as e:
        print(f"Error: Could not find a git repository in this directory. {e}")
        return

    v_pattern = re.compile(r"^v?\d+\.\d+\.\d+")
    
    valid_versions = []
    for tag in repo.tags:
        tag_str = tag.name
        if v_pattern.match(tag_str):
            try:
                # Convert to Version object for SemVer comparison
                v_obj = Version(tag_str)
                valid_versions.append(v_obj)
            except InvalidVersion:
                continue

    valid_versions.sort(reverse=True)

    closest_lower = None
    for v in valid_versions:
        if v < current_v:
            closest_lower = v
            break

    if closest_lower:
        print(f"Current Version: {current_v}")
        print(f"Closest Lower Tag: {closest_lower}")
        with open(os.environ['GITHUB_OUTPUT'], 'a') as f:
            f.write(f"old_version={closest_lower}\n")
    else:
        print(f"Current Version: {current_v}")
        print("No lower SemVer-compliant tags found in the repository.")

if __name__ == "__main__":
    get_closest_lower_tag()
