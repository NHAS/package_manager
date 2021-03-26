# Package Manager

Basic package manage useful for managing cross compiling libraries, and their specific configuration flags.  
  
Extremely basic, I wanted something that would just pull the latest tagged release from github of any repo that I pointed it to.  
And have a basic dependancy system.  
  
The current `example.json` file will build openssh dynamically linked against openssh, and zlib for arm (if you have the toolchain installed, I use crosstools-ng). 
As the current embedded system Im working on doesnt have a version of glibc that I could build. I used the `--rpath` and `--dynamic-linker` options in the openssh build in order to essentially just use another directory for glibc.

But that isnt super relevant. 

# Using
See the `example.json` for how to specifying 'packages' to pull. This just means the most recent tagged item on a github repo.  
After that the program will download, configure and build all libraries.  

# Warnings
So, the package dependancy system is fairly untested and hacked together. PRobably wont stop you from making cyclic dependancies. So just be kind to it and dont do that. 