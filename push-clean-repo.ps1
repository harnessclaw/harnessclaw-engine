cd D:\temp_repo2
git remote add origin https://github.com/harnessclaw/harnessclaw-engine
git push origin main --force
Write-Host "Force push completed. Collaborators should run git fetch + git reset --hard origin/main to sync."
