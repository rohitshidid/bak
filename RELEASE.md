# Release Checklist

This repo is laid out as a Homebrew tap plus source project. You can publish it yourself without Codex pushing anything.

## 1. Choose the GitHub Location

The included formula assumes:

```text
https://github.com/rohitshidid/bak
```

If you use a different account or repo name, update these lines in `Formula/bak.rb` and `README.md`:

```ruby
homepage "https://github.com/rohitshidid/bak"
url "https://github.com/rohitshidid/bak.git", tag: "v0.1.0"
head "https://github.com/rohitshidid/bak.git", branch: "main"
```

## 2. Commit and Tag

```sh
git init
git add .
git commit -m "Initial bak release"
git branch -M main
git tag v0.1.0
```

## 3. Create the Remote and Push

```sh
git remote add origin git@github.com:rohitshidid/bak.git
git push -u origin main
git push origin v0.1.0
```

## 4. Test the Formula

From a fresh terminal:

```sh
brew install --build-from-source ./Formula/bak.rb
brew test bak
bak --version
```

Then test as a tap:

```sh
brew tap rohitshidid/bak git@github.com:rohitshidid/bak.git
brew install rohitshidid/bak/bak
brew test rohitshidid/bak/bak
```

## Notes

- This formula uses a git tag source, so there is no tarball SHA to update.
- If Homebrew audit asks for a fixed revision, add the commit SHA for `v0.1.0`:

```ruby
url "https://github.com/rohitshidid/bak.git",
    tag:      "v0.1.0",
    revision: "<commit-sha>"
```

- Do not sync `~/.bak` to untrusted storage unless you are comfortable with the plaintext contents being present there.
