cmake_minimum_required(VERSION 3.20)

set(CMAKE_OSX_DEPLOYMENT_TARGET "11.0" CACHE STRING "Oldest macOS the plug-in loads on")
set(CMAKE_OSX_ARCHITECTURES "arm64;x86_64" CACHE STRING "Universal: Apple silicon and Intel")

project(PlanckAudio VERSION 1.0.0 LANGUAGES C)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)
set(CMAKE_C_VISIBILITY_PRESET hidden)

configure_file(Info.plist.in ${CMAKE_CURRENT_BINARY_DIR}/Info.plist @ONLY)

add_library(PlanckAudio MODULE src/PlanckAudio.c)
target_compile_options(PlanckAudio PRIVATE -Wall -Wextra -Werror)
target_link_libraries(PlanckAudio PRIVATE "-framework CoreFoundation")
set_target_properties(PlanckAudio PROPERTIES
  BUNDLE TRUE
  BUNDLE_EXTENSION driver
  OUTPUT_NAME PlanckAudio
  MACOSX_BUNDLE_INFO_PLIST ${CMAKE_CURRENT_BINARY_DIR}/Info.plist
)

option(PLANCK_AUDIO_TESTS "Build the user-mode driver tests" ON)
if(PLANCK_AUDIO_TESTS)
  enable_testing()
  add_executable(planck_audio_test tests/driver_test.c src/PlanckAudio.c)
  target_include_directories(planck_audio_test PRIVATE src)
  target_compile_options(planck_audio_test PRIVATE -Wall -Wextra -Werror)
  target_link_libraries(planck_audio_test PRIVATE "-framework CoreFoundation")
  add_test(NAME driver COMMAND planck_audio_test)
endif()
