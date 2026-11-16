// Package osdep isolates everything distro-specific: packages, users, services, SELinux and firewall services.
//
// It is the only package allowed to know which distribution the panel runs on
// (docs/development-plan.md §1.4). CI greps for apt, dnf, ufw, firewalld and
// semanage everywhere else; callers go through OS instead.
package osdep
