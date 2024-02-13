{
  description = "copy-github-to-github";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/23.11";
  };

  outputs = { self, nixpkgs }:
    let
      allSystems = [
        "x86_64-linux" # 64-bit Intel/AMD Linux
        "aarch64-linux" # 64-bit ARM Linux
        "x86_64-darwin" # 64-bit Intel macOS
        "aarch64-darwin" # 64-bit ARM macOS
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs allSystems (system: f {
        inherit system;
        pkgs = import nixpkgs { inherit system; };
      });
    in
    {
      packages = forAllSystems ({ pkgs, ... }: rec {
        default = copy-github-to-github;

        copy-github-to-github = pkgs.buildGo121Module {
          name = "copy-github-to-github";
          src = ./.;
          vendorHash = null;
          CGO_ENABLED = 0;
          flags = [
            "-trimpath"
          ];
          ldflags = [
            "-s"
            "-w"
            "-extldflags -static"
          ];
        };
      });

      # `nix develop` provides a shell containing development tools.
      devShell = forAllSystems ({ system, pkgs }:
        pkgs.mkShell {
          buildInputs = with pkgs; [
            go_1_21
          ];
        });
    };
}

