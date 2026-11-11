{ config, ... }:
{
  networking.hostName = "blossom-vm";

  services.blossom-server = {
    enable = true;
    openFirewall = true;

    settings = {
      host = "0.0.0.0";
      publicDomain = "localhost:3000";

      storage = {
        backend = "local";
        rules = [
          {
            type = "*";
            expiration = "1 week";
          }
        ];
      };

      # Keep the demonstration easy to exercise with curl. Production servers
      # should normally require a valid BUD-11 Nostr authorization event.
      upload.requireAuth = false;
    };
  };

  services.openssh.enable = true;
  networking.firewall.allowedTCPPorts = [ 22 ];

  virtualisation = {
    graphics = false;
    memorySize = 2048;
    cores = 2;
    # Keep the demonstration disposable instead of writing a qcow2 image into
    # the directory from which it was launched.
    diskImage = null;
    forwardPorts = [
      {
        from = "host";
        proto = "tcp";
        host.address = "127.0.0.1";
        host.port = config.services.blossom-server.settings.port;
        guest.port = config.services.blossom-server.settings.port;
      }
      {
        from = "host";
        proto = "tcp";
        host.address = "127.0.0.1";
        host.port = 2222;
        guest.port = 22;
      }
    ];
  };

  system.stateVersion = "25.11";
}
