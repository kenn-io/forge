import {
  siForgejo,
  siGitea,
  siGithub,
  siGitlab,
  type SimpleIcon,
} from "simple-icons";

export const providerIcons: Record<string, SimpleIcon> = {
  forgejo: siForgejo,
  gitea: siGitea,
  github: siGithub,
  gitlab: siGitlab,
};

export function providerIcon(provider: string): SimpleIcon | undefined {
  return providerIcons[provider.trim().toLowerCase()];
}
